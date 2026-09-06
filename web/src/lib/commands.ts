export type Shell = 'linux' | 'powershell' | 'cmd';
export type Protocol = 'http' | 'tcp' | 'udp';
export type CommandOptions = { nonce?: string };

export function validateTarget(rawHost: string, port: number): { host: string; port: number } {
  let host = rawHost.trim().toLowerCase() || 'localhost';
  if (host.includes(':') && host.startsWith('[') && host.endsWith(']')) host = host.slice(1, -1);
  if (host.length > 253 || !Number.isInteger(port) || port < 1 || port > 65535) throw new Error('invalid_target');
  if (host.includes(':')) {
    if (!/^[0-9a-f:.]+$/.test(host)) throw new Error('invalid_target');
    try { new URL(`http://[${host}]/`); } catch { throw new Error('invalid_target'); }
  } else if (/^[0-9.]+$/.test(host)) {
    if (!/^(0|[1-9][0-9]{0,2})(\.(0|[1-9][0-9]{0,2})){3}$/.test(host) || host.split('.').some(part => Number(part) > 255)) throw new Error('invalid_target');
  } else if (host.split('.').some(part => !/^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$/.test(part))) {
    throw new Error('invalid_target');
  }
  return { host, port };
}

function quote(shell: Shell, value: string): string {
  if (/[^a-zA-Z0-9_.:[\]-]/.test(value)) throw new Error('invalid_argument');
  return shell === 'cmd' ? `"${value}"` : `'${value}'`;
}

function command(shell: Shell, mode: 'anonymous' | 'launch', args: string[], install: boolean, options: CommandOptions): string {
  if (!['linux', 'powershell', 'cmd'].includes(shell)) throw new Error('invalid_shell');
  const parameters = `${mode} ${args.map(arg => quote(shell, arg)).join(' ')}`;
  if (!install) return `nt ${parameters}`;
  if (shell === 'linux') return `curl -fsSL https://tunnel.nodelane.net/run.sh | sh -s -- ${parameters}`;
  if (shell === 'powershell') return `& ([scriptblock]::Create((irm 'https://tunnel.nodelane.net/run.ps1'))) -TunnelArguments @(${[mode, ...args].map(arg => quote(shell, arg)).join(', ')})`;
  const nonce = options.nonce ?? Array.from(crypto.getRandomValues(new Uint8Array(16)), byte => byte.toString(16).padStart(2, '0')).join('');
  if (!/^[0-9a-f]{32}$/.test(nonce)) throw new Error('invalid_nonce');
  const directory = `%TEMP%\\nodelane-tunnel-${nonce}`;
  // A successful exclusive mkdir owns this download path; failures never overwrite an existing script.
  return `mkdir "${directory}" && curl.exe -fsSL https://tunnel.nodelane.net/install.cmd -o "${directory}\\install.cmd" && call "${directory}\\install.cmd" ${parameters}`;
}

export function generateAnonymousCommand(shell: Shell, protocol: Protocol, rawHost: string, port: number, install = true, options: CommandOptions = {}): string {
  if (!['http', 'tcp', 'udp'].includes(protocol)) throw new Error('invalid_protocol');
  const target = validateTarget(rawHost, port);
  return command(shell, 'anonymous', [protocol, target.host, String(port)], install, options);
}

export function generateLaunchCommand(shell: Shell, code: string, rawHost: string, port: number, install = true, options: CommandOptions = {}): string {
  if (!/^nlc_[a-z2-7]{26}\.[A-Za-z0-9_-]{42}[AEIMQUYcgkosw048]$/.test(code)) throw new Error('invalid_launch_code');
  const target = validateTarget(rawHost, port);
  return command(shell, 'launch', [code, target.host, String(port)], install, options);
}
