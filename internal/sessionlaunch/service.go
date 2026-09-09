package sessionlaunch

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"time"

	"github.com/neomei/SessionReviewer/internal/atomicfile"
	"github.com/neomei/SessionReviewer/internal/inspect"
	"github.com/neomei/SessionReviewer/internal/pathguard"
	"github.com/neomei/SessionReviewer/internal/strictjson"
	syncengine "github.com/neomei/SessionReviewer/internal/sync"
	"github.com/neomei/SessionReviewer/internal/syncproject"
)

const tokenLifetime = 2 * time.Minute

type Error struct {
	Code    string
	Message string
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}
func launchError(code, message string) error { return &Error{Code: code, Message: message} }

type Request struct {
	DataRoot                  string `json:"data_root" required:"true"`
	ProjectID                 string `json:"project_id" required:"true"`
	Provider                  string `json:"provider" required:"true"`
	SessionID                 string `json:"session_id" required:"true"`
	ExpectedGenerationID      string `json:"expected_generation_id" required:"true"`
	ExpectedSessionViewDigest string `json:"expected_session_view_digest" required:"true"`
}

type BoundSession struct {
	ProjectID          string
	Provider           string
	SessionID          string
	GenerationID       string
	SessionViewDigest  string
	SourceIdentity     string
	SourceRecordDigest string
	ProjectRoot        string
	ProjectIdentity    pathguard.IdentityToken
}

type Response struct {
	SchemaVersion     int    `json:"schema_version"`
	ProjectID         string `json:"project_id"`
	Provider          string `json:"provider"`
	SessionID         string `json:"session_id"`
	GenerationID      string `json:"generation_id"`
	SessionViewDigest string `json:"session_view_digest"`
	State             string `json:"state"`
	launchToken       string
}

type ExecPlan struct {
	Executable         string
	ExecutableIdentity pathguard.IdentityToken
	ExecutableSHA256   string
	Arguments          []string
	WorkingDirectory   string
	ProjectIdentity    pathguard.IdentityToken
}
type Authenticator func(context.Context, Request) (BoundSession, error)
type Options struct {
	GOOS, RuntimeRoot, SelfExecutable string
	Now                               func() time.Time
	Random                            io.Reader
	Authenticate                      Authenticator
	LaunchTerminal                    func(string) error
}
type WorkerOptions struct {
	GOOS, RuntimeRoot, SelfExecutable string
	Now                               func() time.Time
	Authenticate                      Authenticator
}

type envelope struct {
	SchemaVersion int           `json:"schema_version" required:"true"`
	Token         string        `json:"token" required:"true"`
	CreatedAt     string        `json:"created_at" required:"true"`
	ExpiresAt     string        `json:"expires_at" required:"true"`
	Request       Request       `json:"request" required:"true"`
	Configuration Configuration `json:"configuration" required:"true"`
	Self          Configuration `json:"self" required:"true"`
	Bound         envelopeBound `json:"bound" required:"true"`
}
type envelopeBound struct {
	ProjectID          string                  `json:"project_id" required:"true"`
	Provider           string                  `json:"provider" required:"true"`
	SessionID          string                  `json:"session_id" required:"true"`
	GenerationID       string                  `json:"generation_id" required:"true"`
	SessionViewDigest  string                  `json:"session_view_digest" required:"true"`
	SourceIdentity     string                  `json:"source_identity" required:"true"`
	SourceRecordDigest string                  `json:"source_record_digest" required:"true"`
	ProjectRoot        string                  `json:"project_root" required:"true"`
	ProjectIdentity    pathguard.IdentityToken `json:"project_identity" required:"true"`
}

func Open(ctx context.Context, request Request, options Options) (Response, error) {
	if options.GOOS != "darwin" {
		return Response{}, errors.New("native Session launch is unsupported on this operating system")
	}
	if err := validateRequest(request); err != nil {
		return Response{}, err
	}
	options = defaultOptions(options)
	if !cleanAbsolute(options.RuntimeRoot) || !cleanAbsolute(options.SelfExecutable) || options.Authenticate == nil || options.LaunchTerminal == nil {
		return Response{}, errors.New("Session launcher options are invalid")
	}
	configuration, err := LoadConfiguration(request.DataRoot, request.Provider)
	if err != nil {
		return Response{}, launchError("session_launcher_unavailable", "configure and verify this provider launcher before opening the Session")
	}
	bound, err := options.Authenticate(ctx, request)
	if err != nil {
		return Response{}, err
	}
	if err := validateBound(request, bound); err != nil {
		return Response{}, err
	}
	self, err := MeasureConfiguration("codex", "session-reviewer", options.SelfExecutable)
	if err != nil {
		return Response{}, err
	}
	tokenBytes := make([]byte, 32)
	if _, err := io.ReadFull(options.Random, tokenBytes); err != nil {
		return Response{}, errors.New("create launch token")
	}
	token := hex.EncodeToString(tokenBytes)
	now := options.Now().UTC()
	value := envelope{SchemaVersion: 1, Token: token, CreatedAt: now.Format(time.RFC3339Nano), ExpiresAt: now.Add(tokenLifetime).Format(time.RFC3339Nano), Request: request, Configuration: configuration, Self: self, Bound: envelopeBoundFrom(bound)}
	body, err := strictjson.Encode(value)
	if err != nil {
		return Response{}, err
	}
	if err := ensureRuntimeRoot(options.RuntimeRoot); err != nil {
		return Response{}, err
	}
	if err := atomicfile.Write(filepath.Join(options.RuntimeRoot, token+".json"), body, 0o600); err != nil {
		return Response{}, err
	}
	script := filepath.Join(options.RuntimeRoot, token+".command")
	if err := atomicfile.Write(script, []byte(bootstrap(options.SelfExecutable, token)), 0o700); err != nil {
		_ = os.Remove(filepath.Join(options.RuntimeRoot, token+".json"))
		return Response{}, err
	}
	if err := options.LaunchTerminal(script); err != nil {
		_ = os.Remove(script)
		_ = os.Remove(filepath.Join(options.RuntimeRoot, token+".json"))
		return Response{}, err
	}
	return Response{SchemaVersion: 1, ProjectID: request.ProjectID, Provider: request.Provider, SessionID: request.SessionID, GenerationID: request.ExpectedGenerationID, SessionViewDigest: request.ExpectedSessionViewDigest, State: "launch_requested", launchToken: token}, nil
}

func Consume(ctx context.Context, token string, options WorkerOptions) (ExecPlan, error) {
	if options.GOOS != "darwin" {
		return ExecPlan{}, errors.New("native Session launch is unsupported on this operating system")
	}
	if options.Authenticate == nil {
		options.Authenticate = Authenticate
	}
	if !regexpToken(token) || !cleanAbsolute(options.RuntimeRoot) || !cleanAbsolute(options.SelfExecutable) || options.Authenticate == nil {
		return ExecPlan{}, errors.New("launch worker request is invalid")
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if err := ensureRuntimeRoot(options.RuntimeRoot); err != nil {
		return ExecPlan{}, err
	}
	runtimeDirectory, err := pathguard.Open(options.RuntimeRoot)
	if err != nil {
		return ExecPlan{}, errors.New("launch runtime root is unavailable or unsafe")
	}
	defer runtimeDirectory.Close()
	source, claim := token+".json", token+".claimed"
	if err := runtimeDirectory.Root.Rename(source, claim); err != nil {
		return ExecPlan{}, errors.New("launch token is unavailable or already consumed")
	}
	defer runtimeDirectory.Root.Remove(claim)
	body, err := readPrivateRootFile(runtimeDirectory, claim, 64<<10, 0o600)
	if err != nil {
		return ExecPlan{}, errors.New("launch envelope is unavailable or too large")
	}
	var value envelope
	if err := strictjson.Decode(body, &value); err != nil {
		return ExecPlan{}, err
	}
	created, e1 := time.Parse(time.RFC3339Nano, value.CreatedAt)
	expires, e2 := time.Parse(time.RFC3339Nano, value.ExpiresAt)
	if e1 != nil || e2 != nil || value.SchemaVersion != 1 || value.Token != token || !expires.After(created) || expires.Sub(created) != tokenLifetime || options.Now().UTC().Before(created) || !options.Now().UTC().Before(expires) {
		return ExecPlan{}, errors.New("launch envelope expired or invalid")
	}
	self, err := MeasureConfiguration("codex", "session-reviewer", options.SelfExecutable)
	if err != nil || self != value.Self {
		return ExecPlan{}, errors.New("SessionReviewer executable changed before launch")
	}
	configuration, err := LoadConfiguration(value.Request.DataRoot, value.Request.Provider)
	if err != nil || configuration != value.Configuration {
		return ExecPlan{}, errors.New("provider executable changed before launch")
	}
	bound, err := options.Authenticate(ctx, value.Request)
	if err != nil || !reflect.DeepEqual(envelopeBoundFrom(bound), value.Bound) {
		return ExecPlan{}, errors.New("published Session identity changed before launch")
	}
	arguments, err := ProviderArguments(bound.Provider, bound.SessionID, bound.ProjectRoot)
	if err != nil {
		return ExecPlan{}, err
	}
	return ExecPlan{Executable: configuration.Executable, ExecutableIdentity: configuration.Identity, ExecutableSHA256: configuration.SHA256, Arguments: arguments, WorkingDirectory: bound.ProjectRoot, ProjectIdentity: bound.ProjectIdentity}, nil
}

func Authenticate(ctx context.Context, request Request) (BoundSession, error) {
	pinOptions := syncproject.Options{ProjectID: request.ProjectID, DataDir: request.DataRoot, GOOS: runtime.GOOS, Now: time.Now, Trigger: syncengine.TriggerCLI}
	pin, err := syncproject.PinMapping(pinOptions)
	if err != nil {
		return BoundSession{}, err
	}
	defer pin.Close()
	root, identity, err := pin.AuthenticatedProjectRoot()
	if err != nil {
		return BoundSession{}, err
	}
	published, err := inspect.AuthenticatePublishedSessionIdentity(ctx, inspect.PublishedSessionIdentityRequest{DataRoot: request.DataRoot, ProjectID: request.ProjectID, Provider: request.Provider, SessionID: request.SessionID, ExpectedGenerationID: request.ExpectedGenerationID, ExpectedSessionViewDigest: request.ExpectedSessionViewDigest})
	if err != nil {
		return BoundSession{}, err
	}
	if err := pin.Recheck(pinOptions); err != nil {
		return BoundSession{}, err
	}
	return BoundSession{ProjectID: published.ProjectID, Provider: published.Provider, SessionID: published.SessionID, GenerationID: published.GenerationID, SessionViewDigest: published.SessionViewDigest, SourceIdentity: published.SourceIdentity, SourceRecordDigest: published.SourceRecordDigest, ProjectRoot: root, ProjectIdentity: identity}, nil
}

func validateRequest(r Request) error {
	if !cleanAbsolute(r.DataRoot) || r.ProjectID == "" || !supportedProvider(r.Provider) || !nativeSessionIDPattern.MatchString(r.SessionID) || r.ExpectedGenerationID == "" || !digest(r.ExpectedSessionViewDigest) {
		return errors.New("Session launch request is invalid")
	}
	return nil
}
func validateBound(r Request, b BoundSession) error {
	if b.ProjectID != r.ProjectID || b.Provider != r.Provider || b.SessionID != r.SessionID || b.GenerationID != r.ExpectedGenerationID || b.SessionViewDigest != r.ExpectedSessionViewDigest || b.SourceIdentity == "" || !digest(b.SourceRecordDigest) || !cleanAbsolute(b.ProjectRoot) || !b.ProjectIdentity.Valid() {
		return errors.New("authenticated Session identity is invalid")
	}
	return nil
}
func digest(s string) bool {
	if len(s) != 71 || !strings.HasPrefix(s, "sha256:") {
		return false
	}
	for _, c := range s[7:] {
		if c < '0' || c > '9' && c < 'a' || c > 'f' {
			return false
		}
	}
	return true
}
func regexpToken(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' && c < 'a' || c > 'f' {
			return false
		}
	}
	return true
}
func envelopeBoundFrom(b BoundSession) envelopeBound {
	return envelopeBound{b.ProjectID, b.Provider, b.SessionID, b.GenerationID, b.SessionViewDigest, b.SourceIdentity, b.SourceRecordDigest, b.ProjectRoot, b.ProjectIdentity}
}
func defaultOptions(o Options) Options {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Random == nil {
		o.Random = rand.Reader
	}
	if o.Authenticate == nil {
		o.Authenticate = Authenticate
	}
	if o.LaunchTerminal == nil {
		o.LaunchTerminal = launchTerminal
	}
	return o
}
func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
func bootstrap(self, token string) string {
	return "#!/bin/sh\nscript=$0\nrm -f -- \"$script\"\nexec " + shellQuote(self) + " sessions worker --launch-token " + shellQuote(token) + "\n"
}

func pathguardPhysical(file *os.File) (pathguard.IdentityToken, error) {
	return pathguard.PhysicalFileIdentity(file)
}

func ensureRuntimeRoot(root string) error {
	if !cleanAbsolute(root) {
		return errors.New("launch runtime root is invalid")
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return err
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || runtime.GOOS != "windows" && info.Mode().Perm() != 0o700 {
		return errors.New("launch runtime root is unsafe")
	}
	return nil
}
