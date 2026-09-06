import { generateAnonymousCommand, type Protocol, type Shell } from './commands.ts';
import { canonicalLocale } from '../console/model.ts';
import { getTranslation } from '../i18n/index.ts';
import { copyCommand } from '../console/dom.ts';

export function initializeQuickStart(root: HTMLElement, clipboard?: Pick<Clipboard, 'writeText'>, platform = ''): void {
  const { console: copy, errors } = getTranslation(canonicalLocale(root.dataset.locale ?? 'en'));
  const find = <T extends HTMLElement>(selector: string) => root.querySelector<T>(selector)!;
  const protocol = find<HTMLSelectElement>('[data-protocol]'), host = find<HTMLInputElement>('[data-host]'), port = find<HTMLInputElement>('[data-port]');
  const output = find<HTMLElement>('[data-command]'), status = find<HTMLElement>('[data-command-status]'), button = find<HTMLButtonElement>('[data-copy]');
  const fallback = find<HTMLTextAreaElement>('[data-manual]');
  let windows = /Windows/i.test(platform), windowsShell: Shell = 'powershell', command = '';
  const render = () => {
    find<HTMLElement>('[data-shell-group]').hidden = !windows;
    root.querySelectorAll<HTMLButtonElement>('[data-os]').forEach(button => { const selected = button.dataset.os === (windows ? 'windows' : 'linux'); button.classList.toggle('active', selected); button.setAttribute('aria-pressed', String(selected)); });
    root.querySelectorAll<HTMLButtonElement>('[data-shell]').forEach(button => { const selected = button.dataset.shell === windowsShell; button.classList.toggle('active', selected); button.setAttribute('aria-pressed', String(selected)); });
    fallback.hidden = true; fallback.value = ''; status.textContent = '';
    try { command = generateAnonymousCommand(windows ? windowsShell : 'linux', protocol.value as Protocol, host.value, Number(port.value)); output.textContent = command; button.disabled = false; }
    catch { command = ''; output.textContent = ''; status.textContent = errors.invalid_target; button.disabled = true; }
  };
  for (const input of [protocol, host, port]) input.addEventListener('input', render);
  root.querySelectorAll<HTMLButtonElement>('[data-os]').forEach(button => button.addEventListener('click', () => { windows = button.dataset.os === 'windows'; render(); }));
  root.querySelectorAll<HTMLButtonElement>('[data-shell]').forEach(button => button.addEventListener('click', () => { windowsShell = button.dataset.shell as Shell; render(); }));
  button.addEventListener('click', async () => { if (command) status.textContent = await copyCommand(command, fallback, clipboard) ? copy.copied : copy.manual; });
  render();
}
