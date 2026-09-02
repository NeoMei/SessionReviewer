import { constants } from "node:fs";
import { access } from "node:fs/promises";
import { homedir } from "node:os";
import { posix, win32 } from "node:path";
import { CliRunner } from "./runner";

export interface RuntimeDiscoveryOptions {
  legacyCliPath?: string;
  home?: string;
  platform?: NodeJS.Platform;
  env?: NodeJS.ProcessEnv;
  createRunner?: (executable: string) => CliRunner;
  executableExists?: (path: string, platform: NodeJS.Platform) => Promise<boolean>;
}

export interface DiscoveredRuntime {
  runner: CliRunner;
}

export type RuntimeResolver = (options?: RuntimeDiscoveryOptions) => Promise<DiscoveredRuntime | undefined>;

export async function discoverRuntime(options: RuntimeDiscoveryOptions = {}): Promise<DiscoveredRuntime | undefined> {
  const platform = options.platform ?? process.platform;
  const home = options.home ?? homedir();
  const env = options.env ?? process.env;
  const createRunner = options.createRunner ?? ((executable: string) => new CliRunner(executable));
  const exists = options.executableExists ?? executableExists;
  const cliCandidates = executableCandidates("session-reviewer", platform, home, env, options.legacyCliPath, env.SESSIONREVIEWER_CLI_PATH);

  for (const cli of cliCandidates) {
    if (!await exists(cli, platform)) continue;
    try {
      const runner = createRunner(cli);
      await runner.verifyExecutable();
      return { runner };
    } catch {
      continue;
    }
  }
  return undefined;
}

function executableCandidates(
  name: "session-reviewer",
  platform: NodeJS.Platform,
  home: string,
  env: NodeJS.ProcessEnv,
  ...preferred: Array<string | undefined>
): string[] {
  const path = platform === "win32" ? win32 : posix;
  const names = platform === "win32" ? [`${name}.exe`] : [name];
  const pathDirectories = (env.PATH ?? "").split(path.delimiter).filter(Boolean);
  const directories = platform === "win32"
    ? windowsDirectories(name, home, env, pathDirectories)
    : unixDirectories(name, home);
  directories.push(...pathDirectories);
  const candidates = [...preferred, ...directories.flatMap((directory) => names.map((filename) => path.join(directory, filename)))];
  const seen = new Set<string>();
  return candidates.filter((candidate): candidate is string => {
    if (!candidate || !path.isAbsolute(candidate)) return false;
    const identity = platform === "win32" ? candidate.toLowerCase() : candidate;
    if (seen.has(identity)) return false;
    seen.add(identity);
    return true;
  });
}

function unixDirectories(name: "session-reviewer", home: string): string[] {
  return [posix.join(home, ".local", "bin"), "/opt/homebrew/bin", "/usr/local/bin", "/usr/bin"];
}

function windowsDirectories(name: "session-reviewer", home: string, env: NodeJS.ProcessEnv, _pathDirectories: string[]): string[] {
  const localAppData = env.LOCALAPPDATA;
  return localAppData ? [
    win32.join(localAppData, "SessionReviewer", "bin"),
    win32.join(localAppData, "SessionReviewer"),
    win32.join(localAppData, "Programs", "SessionReviewer")
  ] : [];
}

async function executableExists(path: string, platform: NodeJS.Platform): Promise<boolean> {
  try {
    await access(path, platform === "win32" ? constants.F_OK : constants.X_OK);
    return true;
  } catch {
    return false;
  }
}