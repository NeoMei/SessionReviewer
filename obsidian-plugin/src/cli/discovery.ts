import { constants } from "node:fs";
import { access } from "node:fs/promises";
import { homedir } from "node:os";
import { posix, win32 } from "node:path";
import { CliRunner } from "./runner";

export interface RuntimeDiscoveryOptions {
  legacyCliPath?: string;
  legacyAgentPath?: string;
  home?: string;
  platform?: NodeJS.Platform;
  env?: NodeJS.ProcessEnv;
  createRunner?: (executable: string) => CliRunner;
  executableExists?: (path: string, platform: NodeJS.Platform) => Promise<boolean>;
}

export interface DiscoveredRuntime {
  runner: CliRunner;
  agentExecutable: string;
}

export type RuntimeResolver = (options?: RuntimeDiscoveryOptions) => Promise<DiscoveredRuntime | undefined>;

export async function discoverRuntime(options: RuntimeDiscoveryOptions = {}): Promise<DiscoveredRuntime | undefined> {
  const platform = options.platform ?? process.platform;
  const home = options.home ?? homedir();
  const env = options.env ?? process.env;
  const createRunner = options.createRunner ?? ((executable: string) => new CliRunner(executable));
  const exists = options.executableExists ?? executableExists;
  const cliCandidates = executableCandidates("session-reviewer", platform, home, env, options.legacyCliPath, env.SESSIONREVIEWER_CLI_PATH);
  const agentCandidates = executableCandidates("codex", platform, home, env, options.legacyAgentPath, env.SESSIONREVIEWER_AGENT_PATH, env.CODEX_CLI_PATH);
  let fallback: DiscoveredRuntime | undefined;

  for (const cli of cliCandidates) {
    if (!await exists(cli, platform)) continue;
    let runner: CliRunner;
    try {
      runner = createRunner(cli);
      await runner.verifyExecutable();
    } catch {
      continue;
    }
    fallback ??= { runner, agentExecutable: "" };
    for (const agent of agentCandidates) {
      if (!await exists(agent, platform)) continue;
      try {
        const verified = await runner.verifyAgent(agent);
        if (verified.compatible) return { runner, agentExecutable: agent };
      } catch {
        // Continue to the next installed Agent candidate.
      }
    }
  }
  return fallback;
}

function executableCandidates(
  name: "session-reviewer" | "codex",
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
  if (platform === "darwin" && name === "codex") candidates.push("/Applications/ChatGPT.app/Contents/Resources/codex");
  const seen = new Set<string>();
  return candidates.filter((candidate): candidate is string => {
    if (!candidate || !path.isAbsolute(candidate)) return false;
    const identity = platform === "win32" ? candidate.toLowerCase() : candidate;
    if (seen.has(identity)) return false;
    seen.add(identity);
    return true;
  });
}

function unixDirectories(name: "session-reviewer" | "codex", home: string): string[] {
  const result = [posix.join(home, ".local", "bin")];
  if (name === "codex") result.push(posix.join(home, ".npm-global", "bin"));
  result.push("/opt/homebrew/bin", "/usr/local/bin", "/usr/bin");
  return result;
}

function windowsDirectories(name: "session-reviewer" | "codex", home: string, env: NodeJS.ProcessEnv, pathDirectories: string[]): string[] {
  const localAppData = env.LOCALAPPDATA;
  const appData = env.APPDATA;
  if (name === "session-reviewer") {
    return localAppData ? [
      win32.join(localAppData, "SessionReviewer", "bin"),
      win32.join(localAppData, "SessionReviewer"),
      win32.join(localAppData, "Programs", "SessionReviewer")
    ] : [];
  }
  const npmRoots = [appData ? win32.join(appData, "npm") : undefined, ...pathDirectories].filter((directory): directory is string => Boolean(directory));
  const vendorDirectories = npmRoots.flatMap((root) => [
    win32.join(root, "node_modules", "@openai", "codex", "node_modules", "@openai", "codex-win32-x64", "vendor", "x86_64-pc-windows-msvc", "bin"),
    win32.join(root, "node_modules", "@openai", "codex", "node_modules", "@openai", "codex-win32-x64", "vendor", "x86_64-pc-windows-msvc", "codex")
  ]);
  return [
    ...vendorDirectories,
    win32.join(home, ".codex", "packages", "standalone", "current", "bin"),
    ...(localAppData ? [win32.join(localAppData, "Programs", "codex")] : [])
  ];
}

async function executableExists(path: string, platform: NodeJS.Platform): Promise<boolean> {
  try {
    await access(path, platform === "win32" ? constants.F_OK : constants.X_OK);
    return true;
  } catch {
    return false;
  }
}
