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
    protocolLabel: string;
    copy: string;
    copied: string;
    selected: string;
    anonymous: string;
    forwarding: string;
    publicLabels: {
      http: string;
      tcp: string;
      udp: string;
    };
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
      ComparisonRowTranslation,
    ];
    recommended: string;
    anonymousUse: string;
    accountUse: string;
    safetyLabel: string;
    safety: string;
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
