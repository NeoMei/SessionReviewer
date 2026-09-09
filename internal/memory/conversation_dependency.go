package memory

import (
	"errors"
	"fmt"
)

// ConversationChainDependency binds one immutable retained conversation chain
// to the exact SessionView whose visible messages and selected facts it
// authenticates. It lives in memory so provider contracts remain independent
// of the conversationchain package.
type ConversationChainDependency struct {
	Provider          string `json:"provider"`
	SessionID         string `json:"session_id"`
	SessionViewDigest string `json:"session_view_digest"`
	Digest            string `json:"digest"`
}

func validateConversationChainDependencies(value GenerationManifest, checkpoints ...func() error) error {
	if len(value.ConversationChains)+len(value.RetainedConversationChains) > 65536 {
		return errors.New("generation conversation chain dependency limit 65536 exceeded")
	}
	cumulative := make(map[string]map[string]struct{}, len(value.SessionViews)+len(value.RetainedSessionViews))
	for _, dependency := range append(append([]SessionViewDependency(nil), value.SessionViews...), value.RetainedSessionViews...) {
		key := dependency.Provider + "\x00" + dependency.SessionID
		if cumulative[key] == nil {
			cumulative[key] = make(map[string]struct{})
		}
		cumulative[key][dependency.Digest] = struct{}{}
	}

	currentIdentities := make(map[string]struct{}, len(value.ConversationChains))
	seenTuples := make(map[string]struct{}, len(value.ConversationChains)+len(value.RetainedConversationChains))
	chainBindings := make(map[string]string, len(seenTuples))
	viewBindings := make(map[string]string, len(seenTuples))
	validate := func(dependency ConversationChainDependency, historical bool) error {
		if err := digestCheckpoint(checkpoints); err != nil {
			return err
		}
		if !safeIDPattern.MatchString(dependency.Provider) || !sessionIDPattern.MatchString(dependency.SessionID) || !validDigest(dependency.SessionViewDigest) || !validDigest(dependency.Digest) {
			return errors.New("invalid conversation chain dependency")
		}
		identity := dependency.Provider + "\x00" + dependency.SessionID
		views, exists := cumulative[identity]
		if !exists {
			return errors.New("conversation chain identity is absent from cumulative Session set")
		}
		if historical {
			if _, currentView := views[dependency.SessionViewDigest]; currentView {
				return errors.New("historical conversation chain impersonates a cumulative SessionView root")
			}
		} else {
			if _, exact := views[dependency.SessionViewDigest]; !exact {
				return errors.New("current conversation chain does not match a cumulative SessionView dependency")
			}
			if _, duplicate := currentIdentities[identity]; duplicate {
				return fmt.Errorf("duplicate current conversation chain identity %q", dependency.SessionID)
			}
			currentIdentities[identity] = struct{}{}
		}
		tuple := identity + "\x00" + dependency.SessionViewDigest
		if _, duplicate := seenTuples[tuple]; duplicate {
			return errors.New("duplicate conversation chain SessionView binding")
		}
		seenTuples[tuple] = struct{}{}
		if bound, duplicate := chainBindings[dependency.Digest]; duplicate && bound != tuple {
			return errors.New("conversation chain digest has conflicting identity bindings")
		}
		chainBindings[dependency.Digest] = tuple
		if bound, duplicate := viewBindings[dependency.SessionViewDigest]; duplicate && bound != identity {
			return errors.New("SessionView digest has conflicting conversation identities")
		}
		viewBindings[dependency.SessionViewDigest] = identity
		return nil
	}
	for _, dependency := range value.ConversationChains {
		if err := validate(dependency, false); err != nil {
			return err
		}
	}
	for _, dependency := range value.RetainedConversationChains {
		if err := validate(dependency, true); err != nil {
			return err
		}
	}
	return nil
}
