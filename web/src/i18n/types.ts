export interface Translation {
  meta: {
    title: string;
    description: string;
  };
  skip: string;
  brandHome: string;
  navLabel: string;
  nav: {
    start: string;
    protocols: string;
    access: string;
  };
  languageLabel: string;
  mainSite: string;
  mainSiteLabel: string;
  feedback: {
    label: string;
    menuLabel: string;
    discussion: string;
    issue: string;
    security: string;
  };
  railLabel: string;
  hero: {
    topline: string;
    eyebrow: string;
    titleBefore: string;
    titleAccent: string;
    titleAfter: string;
    leadStrong: string;
    lead: string;
    factsLabel: string;
    facts: readonly [TranslationFact, TranslationFact, TranslationFact];
    scroll: string;
    scrollLabel: string;
  };
  quickstart: {
    ariaLabel: string;
    title: string;
    subtitle: string;
    osLabel: string;
    shellLabel: string;
    copy: string;
    copied: string;
    selected: string;
    anonymous: string;
    afterRun: string;
  };
  protocols: {
    topline: string;
    kicker: string;
    title: string;
    lead: string;
    available: string;
    items: readonly [ProtocolTranslation, ProtocolTranslation, ProtocolTranslation];
    useCasesLabel: string;
    useCases: readonly [string, string, string, string];
  };
  access: {
    topline: string;
    kicker: string;
    title: string;
    lead: string;
    comparisonLabel: string;
    legend: string;
    anonymous: PlanTranslation;
    account: PlanTranslation;
    rows: readonly [
      ComparisonRowTranslation,
      ComparisonRowTranslation,
      ComparisonRowTranslation,
      ComparisonRowTranslation,
      ComparisonRowTranslation,
      ComparisonRowTranslation,
    ];
    recommended: string;
    anonymousUse: string;
    accountUse: string;
    safetyLabel: string;
    safety: string;
  };
  console: {
    title: string;
    permanent: string;
    deleted: string;
    newRoute: string;
    domain: string;
    status: string;
    currentRun: string;
    connections: string;
    upload: string;
    download: string;
    actions: string;
    loading: string;
    emptyActive: string;
    emptyDeleted: string;
    create: string;
    host: string;
    port: string;
    protocol: string;
    copy: string;
    copied: string;
    manual: string;
    expires: string;
    singleUse: string;
    expired: string;
    stop: string;
    delete: string;
    restore: string;
    cancel: string;
    confirm: string;
    stopConfirm: string;
    deleteConfirm: string;
    restoreConfirm: string;
    safetyLimit: string;
    unavailable: string;
    notObserved: string;
    partial: string;
    never: string;
    starting: string;
    online: string;
    stopping: string;
    offline: string;
    stop_timeout: string;
    routeDeleted: string;
    nameReleased: string;
    details: string;
    back: string;
    retry: string;
    refresh: string;
    logout: string;
    loggedOut: string;
    language: string;
    localTarget: string;
    launch: string;
    runActive: string;
    deleteZone: string;
    until: string;
    remaining: string;
    account: string;
    identitySettings: string;
    anonymous: string;
    login: string;
    available: string;
    unlimited: string;
    randomName: string;
    permanentName: string;
    retention: string;
    created: string;
    open: string;
    loadingCode: string;
  };
  errors: {
    subdomain_invalid: string;
    subdomain_reserved: string;
    subdomain_conflict: string;
    route_limit_reached: string;
    route_not_found: string;
    invalid_target: string;
    dependency_unavailable: string;
    insufficient_scope: string;
    rate_limited: string;
    unauthorized: string;
    invalid_request: string;
    route_deleted: string;
    run_already_active: string;
    run_stopped: string;
    idempotency_conflict: string;
    launch_code_expired: string;
    launch_code_used: string;
    launch_code_revoked: string;
  };
  footer: {
    slogan: string;
    backToTop: string;
  };
}

interface TranslationFact {
  term: string;
  value: string;
}

interface ProtocolTranslation {
  id: string;
  name: string;
  description: string;
  command: string;
}

interface PlanTranslation {
  status: string;
  title: string;
  description: string;
}

interface ComparisonRowTranslation {
  term: string;
  anonymous: string;
  account: string;
  better?: boolean;
  unavailable?: boolean;
}

export function defineTranslation(translation: Translation): Translation {
  return translation;
}
