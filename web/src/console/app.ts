import { ConsoleAPI, APIError, type LaunchCode } from './api.ts';
import { getTranslation, type Translation } from '../i18n/index.ts';
import { activeRun, canonicalLocale, consoleLocation, consoleURL, formatBytes, formatNumber, localeURL, loginURL, runState, safeLogoutURL, safePublicURL, summarizeStats, validRouteID, validateSubdomain, type Route, type Stats } from './model.ts';
import { LatestRequest, pollVisible } from './polling.ts';
import { copyCommand, routeRow, updateRouteRow, text } from './dom.ts';
import { generateLaunchCommand, validateTarget, type Shell } from '../lib/commands.ts';
import { errorText } from './errors.ts';
import { mountIdentitySettings } from './identity-settings.ts';

export function mountConsole(): void {
  const page = consoleLocation(window.location.href);
  const locale = page.locale;
  const { console: copy, errors } = getTranslation(locale);
  const api = new ConsoleAPI();
  const latest = new LatestRequest();
  const element = <T extends HTMLElement = HTMLElement>(id: string) => document.getElementById(id) as T;
  const show = (id: string, visible: boolean) => { element(id).hidden = !visible; };
  const set = (id: string, value: string) => { element(id).textContent = value; };
  const alert = element('page-alert');
  element<HTMLDialogElement>('confirm-dialog').addEventListener('keydown', event => {
    if (event.key === 'Escape') { event.preventDefault(); element<HTMLDialogElement>('confirm-dialog').close('cancel'); }
  });
  let routes: Route[] = [];
  let current: Route | null = null;
  let activeCount = 0;
  let shell: Shell = /Windows/i.test(navigator.userAgent) ? 'powershell' : 'linux';
  let issued: LaunchCode | null = null;
  let commandNonce = '';
  let mutation = false;
  let operationError = false;
  let createKey = { subdomain: '', key: '' };
  let issuer = '';
  let publicDomain = '';
  let stopPolling = () => {};
  let codeFeedback: 'copied' | 'manual' | '' = '';

  const showError = (error: unknown, sticky = false) => {
    operationError = sticky;
    alert.textContent = errorText(error, locale); alert.hidden = false;
  };
  const handleError = (error: unknown, sticky = false) => {
    if (error instanceof APIError && (error.status === 401 || error.code === 'unauthorized')) {
      clearCommand(); stopPolling(); window.location.assign(loginURL(window.location.href, locale)); return;
    }
    showError(error, sticky);
  };
  function clearCommand(): void {
    issued = null; commandNonce = ''; codeFeedback = ''; set('launch-command', ''); set('launch-feedback', '');
    element<HTMLTextAreaElement>('manual-command').value = ''; show('manual-command', false); show('command-display', false);
  }
  const target = () => validateTarget(element<HTMLInputElement>('local-host').value, Number(element<HTMLInputElement>('local-port').value));
  const renderCommand = () => {
    document.querySelectorAll<HTMLButtonElement>('[data-command-shell]').forEach(button => button.setAttribute('aria-pressed', String(button.dataset.commandShell === shell)));
    if (!issued) return;
    const seconds = Math.max(0, Math.ceil((Date.parse(issued.expires_at) - Date.now()) / 1000));
    if (!seconds) { clearCommand(); set('launch-feedback', copy.expired); return; }
    try {
      const local = target();
      const command = generateLaunchCommand(shell, issued.launch_code, local.host, local.port, true, { nonce: commandNonce });
      set('launch-command', command); element<HTMLTextAreaElement>('manual-command').value = command; show('command-display', true);
      set('launch-feedback', `${codeFeedback === 'copied' ? `${copy.copied} · ` : ''}${copy.singleUse} · ${copy.expires} ${Math.floor(seconds / 60)}:${String(seconds % 60).padStart(2, '0')}`);
    } catch { clearCommand(); set('launch-feedback', errors.invalid_target); }
  };
  const disableCreate = () => {
    const blocked = activeCount >= 5;
    const link = element<HTMLAnchorElement>('new-route-link');
    link.setAttribute('aria-disabled', String(blocked)); link.tabIndex = blocked ? -1 : 0; link.title = blocked ? errors.route_limit_reached : copy.newRoute;
    element<HTMLButtonElement>('create-route').disabled = blocked || mutation;
    show('empty-create', !blocked && page.view !== 'deleted');
  };
  const renderStats = (stats: Stats | null) => {
    const available = stats?.availability === 'available';
    set('detail-connections', formatNumber(available ? stats.current_connections : null, locale));
    set('detail-upload', formatBytes(available ? stats.upload_bytes_today : null, locale));
    set('detail-download', formatBytes(available ? stats.download_bytes_today : null, locale));
    set('stats-availability', stats?.availability === 'not_observed' ? copy.notObserved : available ? copy.available : copy.unavailable);
    set('observed-at', available && stats.observed_at && Number.isFinite(Date.parse(stats.observed_at)) ? new Intl.DateTimeFormat(locale, { dateStyle: 'medium', timeStyle: 'medium', timeZone: 'UTC' }).format(new Date(stats.observed_at)) + ' UTC' : '');
  };
  const renderList = (list: Route[], samples: (Stats | null)[]) => {
    const deleted = page.view === 'deleted';
    routes = list;
    const headings = deleted ? [copy.domain,copy.status,copy.until,copy.remaining,copy.actions] : [copy.domain,copy.status,copy.connections,copy.upload + ' (UTC)',copy.download + ' (UTC)',copy.actions];
    element('route-head').replaceChildren(...headings.map(heading => text(document, 'th', heading)));
    const body = element<HTMLTableSectionElement>('route-rows');
    const focused = body.contains(document.activeElement) ? document.activeElement as HTMLElement : null;
    const existing = new Map(Array.from(body.querySelectorAll<HTMLTableRowElement>('tr'), row => [row.dataset.routeId, row]));
    const rows = list.map((route, index) => {
      const row = existing.get(route.id);
      existing.delete(route.id);
      if (!row) return routeRow(document, route, samples[index] ?? null, copy, locale, deleted, publicDomain);
      updateRouteRow(row, route, samples[index] ?? null, copy, locale, deleted, publicDomain);
      return row;
    });
    existing.forEach(row => row.remove());
    rows.forEach((row, index) => { if (body.children[index] !== row) body.insertBefore(row, body.children[index] ?? null); });
    if (focused?.isConnected && document.activeElement !== focused) focused.focus();
    const icon = document.getElementById('restore-icon') as HTMLTemplateElement;
    document.querySelectorAll('[data-restore]').forEach(button => { if (!button.firstElementChild) button.prepend(icon.content.cloneNode(true)); });
    element('active-tab').setAttribute('aria-current', deleted ? 'false' : 'page');
    element('deleted-tab').setAttribute('aria-current', deleted ? 'page' : 'false');
    set('route-count', `${activeCount} / 5`);
    set('empty-title', deleted ? copy.emptyDeleted : copy.emptyActive);
    show('empty-list', list.length === 0); show('route-table', list.length > 0); show('account-stats', !deleted); show('empty-create', !deleted && activeCount < 5);
    const total = summarizeStats(samples);
    set('online-count', formatNumber(list.filter(route => route.current_run?.status === 'online').length, locale));
    set('total-connections', formatNumber(total.connections, locale)); set('total-upload', formatBytes(total.upload, locale)); set('total-download', formatBytes(total.download, locale));
    show('partial-stats', !deleted && total.partial); disableCreate();
  };
  const renderDetail = (route: Route, stats: Stats | null) => {
    current = route;
    const deleted = route.status === 'deleted';
    const running = activeRun(route);
    const state = runState(route);
    set('detail-domain', `${route.subdomain}.${publicDomain}`);
    set('route-status', deleted ? copy.routeDeleted : copy.permanent);
    set('detail-run-state', copy[state as keyof Translation['console']] ?? copy.unavailable);
    element('detail-run-state').className = `state state-${state}`;
    set('detail-run-id', route.current_run?.id ?? '');
    const publicURL = safePublicURL(route.public_url, route.subdomain, publicDomain);
    const publicLink = element<HTMLAnchorElement>('public-link');
    if (publicURL && !deleted) { publicLink.href = publicURL; publicLink.hidden = false; } else { publicLink.removeAttribute('href'); publicLink.hidden = true; }
    element<HTMLAnchorElement>('detail-back').href = consoleURL(locale, '', deleted);
    show('deleted-notice', deleted); show('launch-section', !deleted); show('delete-route', !deleted);
    if (deleted) set('recover-until', route.recoverable_until ? new Intl.DateTimeFormat(locale, { dateStyle: 'medium', timeStyle: 'short' }).format(new Date(route.recoverable_until)) : copy.nameReleased);
    element<HTMLButtonElement>('detail-restore').disabled = mutation || activeCount >= 5 || !!route.name_released_at || !route.recoverable_until || Date.parse(route.recoverable_until) <= Date.now();
    element<HTMLButtonElement>('copy-launch').disabled = mutation || running || deleted;
    element<HTMLButtonElement>('stop-run').disabled = mutation || !running || state === 'stopping' || state === 'stop_timeout';
    element<HTMLButtonElement>('delete-route').disabled = mutation;
    show('active-run-note', running && !deleted);
    if (running || deleted) clearCommand();
    renderCommand(); renderStats(stats);
  };
  const sample = async (id: string, signal: AbortSignal): Promise<Stats | null> => {
    try { return await api.stats(id, signal); }
    catch (error) { if (signal.aborted || error instanceof APIError && error.status === 401) throw error; return null; }
  };
  const refresh = async (): Promise<void> => {
    if (mutation || document.hidden) return;
    try {
      await latest.run(async signal => {
        const active = await api.routes(false, signal);
        if (page.view === 'new') return { active, list: [], samples: [], route: null, stats: null };
        if (page.view === 'detail') {
          const route = await api.route(page.routeID, signal);
          const stats = await sample(page.routeID, signal);
          return { active, list: [], samples: [], route, stats };
        }
        const list = page.view === 'deleted' ? await api.routes(true, signal) : active;
        const samples = page.view === 'deleted' ? [] : await Promise.all(list.map(route => sample(route.id, signal)));
        return { active, list, samples, route: null, stats: null };
      }, data => {
        activeCount = data.active.length; disableCreate(); show('page-loading', false); show('page-retry', false);
        if (!operationError) alert.hidden = true;
        if (data.route) renderDetail(data.route, data.stats);
        else if (page.view !== 'new') renderList(data.list, data.samples);
        else if (activeCount >= 5) { set('domain-validation', errors.route_limit_reached); element('domain-validation').classList.add('error'); }
        show('list-view', page.view === 'active' || page.view === 'deleted'); show('new-view', page.view === 'new'); show('detail-view', page.view === 'detail');
      });
    } catch (error) {
      show('page-loading', false); show('page-retry', true);
      if (current) renderStats(null);
      if (page.view === 'active' && routes.length) renderList(routes, routes.map(() => null));
      handleError(error);
    }
  };
  const confirm = (action: 'stop' | 'delete' | 'restore', route: Route): Promise<boolean> => {
    const dialog = element<HTMLDialogElement>('confirm-dialog');
    set('confirm-title', copy[action]); set('confirm-domain', `${route.subdomain}.${publicDomain}`);
    set('confirm-description', copy[`${action}Confirm`]); set('confirm-action', copy[action]);
    const focused = document.activeElement as HTMLElement | null;
    return new Promise(resolve => {
      dialog.addEventListener('close', () => { focused?.focus(); resolve(dialog.returnValue === 'confirm'); }, { once: true });
      dialog.returnValue = ''; dialog.showModal();
    });
  };
  const operate = async (action: 'stop' | 'delete' | 'restore', route: Route) => {
    if (mutation || !await confirm(action, route)) return;
    mutation = true; latest.cancel(); operationError = false; alert.hidden = true;
    ['stop-run','delete-route','detail-restore','copy-launch'].forEach(id => { element<HTMLButtonElement>(id).disabled = true; });
    document.querySelectorAll<HTMLButtonElement>('[data-restore]').forEach(button => { button.disabled = true; });
    let failed = false;
    try {
      await api[action](route.id); clearCommand();
      if (action === 'delete') { window.location.assign(consoleURL(locale, '', true)); return; }
      if (action === 'restore') { window.location.assign(consoleURL(locale, route.id)); return; }
    } catch (error) { failed = true; handleError(error, true); }
    finally {
      mutation = false;
      if (!failed) await refresh();
      else if (current) renderDetail(current, null);
      else renderList(routes, []);
    }
  };

  element<HTMLSelectElement>('console-language').addEventListener('change', event => window.location.assign(localeURL(window.location.href, canonicalLocale((event.target as HTMLSelectElement).value))));
  document.querySelectorAll('[data-refresh]').forEach(button => button.addEventListener('click', () => { operationError = false; void refresh(); }));
  element('page-retry').addEventListener('click', () => void boot());
  element('new-route-link').addEventListener('click', event => { if (activeCount >= 5) event.preventDefault(); });
  element('route-rows').addEventListener('click', event => {
    const button = (event.target as Element).closest<HTMLButtonElement>('[data-restore]');
    const route = routes.find(route => route.id === button?.dataset.restore);
    if (route) void operate('restore', route);
  });
  for (const [id, action] of [['stop-run','stop'],['delete-route','delete'],['detail-restore','restore']] as const) element(id).addEventListener('click', () => { if (current) void operate(action, current); });
  element<HTMLInputElement>('subdomain').addEventListener('input', event => {
    const input = event.target as HTMLInputElement;
    const error = validateSubdomain(input.value);
    input.setAttribute('aria-invalid', String(!!error));
    set('domain-validation', error ? errors[error] : `${input.value}.${publicDomain}`);
    element('domain-validation').classList.toggle('error', !!error);
  });
  element<HTMLFormElement>('create-form').addEventListener('submit', async event => {
    event.preventDefault(); if (mutation || activeCount >= 5) return;
    const subdomain = element<HTMLInputElement>('subdomain').value;
    const error = validateSubdomain(subdomain);
    if (error) { set('domain-validation', errors[error]); element('subdomain').focus(); return; }
    if (createKey.subdomain !== subdomain) createKey = { subdomain, key: crypto.randomUUID() };
    mutation = true; disableCreate(); operationError = false; alert.hidden = true;
    try {
      const result = await api.create(subdomain, createKey.key);
      if (!validRouteID(result?.route?.id ?? '')) throw new APIError('dependency_unavailable');
      window.location.assign(consoleURL(locale, result.route.id));
    } catch (error) { handleError(error, true); }
    finally { mutation = false; disableCreate(); }
  });
  document.querySelectorAll<HTMLButtonElement>('[data-command-shell]').forEach(button => button.addEventListener('click', () => { shell = button.dataset.commandShell as Shell; codeFeedback = ''; renderCommand(); }));
  for (const id of ['local-host','local-port']) element(id).addEventListener('input', () => { codeFeedback = ''; renderCommand(); });
  element<HTMLFormElement>('launch-form').addEventListener('submit', async event => {
    event.preventDefault(); if (mutation || !current || current.status !== 'active' || activeRun(current)) return;
    try { target(); } catch { set('launch-feedback', errors.invalid_target); element('local-host').focus(); return; }
    mutation = true; latest.cancel(); operationError = false; element<HTMLButtonElement>('copy-launch').disabled = true; set('copy-launch-label', copy.loadingCode); alert.hidden = true;
    try {
      const value = await api.launch(current.id);
      if (value.route_id !== current.id || !Number.isFinite(Date.parse(value.expires_at)) || Date.parse(value.expires_at) <= Date.now()) throw new APIError('dependency_unavailable');
      commandNonce = crypto.randomUUID().replaceAll('-', '');
      const command = generateLaunchCommand(shell, value.launch_code, target().host, target().port, true, { nonce: commandNonce });
      issued = value;
      codeFeedback = await copyCommand(command, element<HTMLTextAreaElement>('manual-command'), navigator.clipboard) ? 'copied' : 'manual';
      renderCommand();
    } catch (error) { clearCommand(); handleError(error, true); }
    finally { mutation = false; element<HTMLButtonElement>('copy-launch').disabled = !!current && activeRun(current); set('copy-launch-label', copy.copy); }
  });
  element('logout').addEventListener('click', async () => {
    if (mutation) return; mutation = true; latest.cancel(); clearCommand(); stopPolling();
    let localConfirmed = false;
    let resumeUpdates = false;
    try {
      const result = await api.logout();
      localConfirmed = result.logged_out === true;
      const target = safeLogoutURL(result.end_session_url, issuer);
      if (localConfirmed && target) { window.location.assign(target); return; }
      throw new Error('logout_unconfirmed');
    } catch (error) {
      if (localConfirmed) { show('list-view', false); show('detail-view', false); show('new-view', false); alert.textContent = copy.loggedOut; alert.hidden = false; }
      else {
        try {
          await api.session();
          showError(error, true); resumeUpdates = true;
        } catch (sessionError) { show('page-retry', true); handleError(sessionError); }
      }
    }
    finally {
      mutation = false;
      if (resumeUpdates) stopPolling = pollVisible(document, refresh, () => latest.cancel());
    }
  });
  async function boot(): Promise<void> {
    stopPolling(); operationError = false; alert.hidden = true; show('page-retry', false); show('page-loading', true);
    if (page.view === 'invalid') { show('page-loading', false); alert.textContent = errors.route_not_found; alert.hidden = false; return; }
    try {
      const session = await api.session();
      set('account-name', session.name || session.email || copy.account);
      const config = await api.config(); issuer = config.oidc.issuer; publicDomain = config.public_domain;
      mountIdentitySettings(document, config.oidc.issuer, locale, window.location.href);
      set('domain-suffix', '.' + publicDomain);
      stopPolling = pollVisible(document, refresh, () => latest.cancel());
    } catch (error) { show('page-loading', false); show('page-retry', true); handleError(error); }
  }
  const countdown = window.setInterval(() => { if (!document.hidden && issued) renderCommand(); }, 1000);
  window.addEventListener('pagehide', () => { stopPolling(); clearCommand(); window.clearInterval(countdown); }, { once: true });
  renderCommand(); void boot();
}
