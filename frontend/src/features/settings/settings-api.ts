import { apiRequest } from "@/shared/api/client";
import {
  createObjectDecoder,
  decodeBooleanResult,
  hasShape,
  isArrayOf,
  isBoolean,
  isNumber,
  isObject,
  isOneOf,
  isOptional,
  isString,
  isStringOrNumber,
  type ApiDecoder,
} from "@/shared/api/decoder";
import type { SortOrder } from "@/shared/lib/table-sort";

export type SettingsConfigDTO = {
  providerBuild: { baseURL: string; clientVersion: string; clientIdentifier: string; tokenAuth: string; tokenAuthConfigured: boolean; userAgent: string };
  providerWeb: {
    baseURL: string; quotaTimeout: string; chatTimeout: string; imageTimeout: string; videoTimeout: string;
    statsigMode: "manual" | "url"; statsigManualValue?: string; statsigManualConfigured: boolean; statsigSignerURL: string;
    mediaConcurrency: number; allowNSFW: boolean;
    recoveryBackoffBase: string; recoveryBackoffMax: string;
  };
  providerConsole: { baseURL: string; userAgent: string; chatTimeout: string };
  proactiveUpstreamSync: {
    billing: boolean;
    webQuota: boolean;
    modelCatalogCatchup: boolean;
    allowManualBillingRefresh: boolean;
    allowManualQuotaRefresh: boolean;
  };
  batch: { importConcurrency: number; conversionConcurrency: number; syncConcurrency: number; refreshConcurrency: number; randomDelay: string };
  media: {
    maxImageBytes: number; maxTotalBytes: number; cleanupThresholdPercent: number;
    cleanupInterval: string;
  };
  routing: {
    stickyTTL: string; cooldownBase: string; cooldownMax: string; capacityWait: string; maxAttempts: number;
    retryStatusCodes: number[]; retryServerErrors: boolean;
  };
  audit: { bufferSize: number; batchSize: number; flushInterval: string };
  clientKeyDefaults: { rpmLimit: number; maxConcurrent: number };
};

export type EgressNodeDTO = {
  id: string; name: string; scope: EgressScope; scopes: EgressScope[]; enabled: boolean;
  proxyConfigured: boolean; proxyProtocol?: string; userAgent: string; cookieConfigured: boolean;
  health: number; failureCount: number; cooldownUntil?: string; lastError?: string;
  successCount: number; requestCount: number; successRate: number; failureRate: number;
  inflight: number; lastProbeAt?: string; lastProbeOK?: boolean; lastProbeMs?: number; lastProbeError?: string;
};

export type EgressNodeInput = {
  name: string; scope: EgressScope; scopes: EgressScope[]; enabled: boolean; proxyURL?: string;
  clearProxyURL?: boolean; userAgent: string; cloudflareCookies?: string; clearCookies?: boolean;
};

export type EgressBatchImportInput = {
  namePrefix: string;
  scope: EgressScope;
  scopes: EgressScope[];
  enabled: boolean;
  proxyText: string;
  userAgent?: string;
  cloudflareCookies?: string;
};

export type EgressBatchImportResultDTO = {
  created: number;
  failed: number;
  skipped: number;
  errors: string[];
  items: EgressNodeDTO[];
};

export type EgressScope = "grok_build" | "grok_web" | "grok_console" | "grok_web_asset";
export type EgressNodeListDTO = { items: EgressNodeDTO[]; defaultUserAgents: Record<EgressScope, string> };

/** Known scopes; unknown values are mapped to grok_web so one bad row cannot blank the whole list. */
export const EGRESS_SCOPES = ["grok_build", "grok_web", "grok_console", "grok_web_asset"] as const;

function coerceString(value: unknown, fallback = ""): string {
  if (typeof value === "string") return value;
  if (typeof value === "number" && Number.isFinite(value)) return String(value);
  return fallback;
}

function coerceNumber(value: unknown, fallback = 0): number {
  if (typeof value === "number" && Number.isFinite(value)) return value;
  if (typeof value === "string" && value.trim() !== "") {
    const parsed = Number(value);
    if (Number.isFinite(parsed)) return parsed;
  }
  return fallback;
}

function coerceBoolean(value: unknown, fallback = false): boolean {
  if (typeof value === "boolean") return value;
  return fallback;
}

function coerceScope(value: unknown): EgressScope {
  if (typeof value === "string" && (EGRESS_SCOPES as readonly string[]).includes(value)) {
    return value as EgressScope;
  }
  // Legacy / unknown scopes: keep list visible rather than failing the entire page.
  return "grok_web";
}

function coerceScopes(raw: unknown, primary: unknown): EgressScope[] {
  const out: EgressScope[] = [];
  const seen = new Set<string>();
  const push = (value: unknown) => {
    const scope = coerceScope(value);
    if (seen.has(scope)) return;
    // Only accept known values from explicit strings; coerceScope maps unknown → grok_web.
    if (typeof value === "string" && !(EGRESS_SCOPES as readonly string[]).includes(value) && value !== "") return;
    seen.add(scope);
    out.push(scope);
  };
  if (Array.isArray(raw)) {
    for (const item of raw) push(item);
  } else if (typeof raw === "string" && raw.includes(",")) {
    for (const part of raw.split(",")) push(part.trim());
  }
  if (out.length === 0) push(primary);
  if (out.length === 0) out.push("grok_web");
  return out;
}

function normalizeEgressNode(raw: unknown): EgressNodeDTO | null {
  if (!isObject(raw)) return null;
  const record = raw as Record<string, unknown>;
  const id = coerceString(record.id);
  const name = coerceString(record.name);
  if (!id || !name) return null;
  const scopes = coerceScopes(record.scopes ?? record.scope, record.scope);
  return {
    id,
    name,
    scope: scopes[0] ?? coerceScope(record.scope),
    scopes,
    enabled: coerceBoolean(record.enabled, true),
    proxyConfigured: coerceBoolean(record.proxyConfigured),
    proxyProtocol: typeof record.proxyProtocol === "string" ? record.proxyProtocol : undefined,
    userAgent: coerceString(record.userAgent),
    cookieConfigured: coerceBoolean(record.cookieConfigured),
    health: coerceNumber(record.health, 1),
    failureCount: coerceNumber(record.failureCount),
    cooldownUntil: typeof record.cooldownUntil === "string" ? record.cooldownUntil : undefined,
    lastError: typeof record.lastError === "string" ? record.lastError : undefined,
    successCount: coerceNumber(record.successCount),
    requestCount: coerceNumber(record.requestCount),
    successRate: coerceNumber(record.successRate),
    failureRate: coerceNumber(record.failureRate),
    inflight: coerceNumber(record.inflight),
    lastProbeAt: typeof record.lastProbeAt === "string" ? record.lastProbeAt : undefined,
    lastProbeOK: typeof record.lastProbeOK === "boolean" ? record.lastProbeOK : undefined,
    lastProbeMs: typeof record.lastProbeMs === "number" ? record.lastProbeMs : undefined,
    lastProbeError: typeof record.lastProbeError === "string" ? record.lastProbeError : undefined,
  };
}

function normalizeDefaultUserAgents(raw: unknown): Record<EgressScope, string> {
  const record = isObject(raw) ? (raw as Record<string, unknown>) : {};
  return {
    grok_build: coerceString(record.grok_build),
    grok_web: coerceString(record.grok_web),
    grok_console: coerceString(record.grok_console),
    grok_web_asset: coerceString(record.grok_web_asset ?? record.grok_web),
  };
}

const decodeEgressNodeResilient: ApiDecoder<EgressNodeDTO> = (value) => {
  const node = normalizeEgressNode(value);
  if (!node) throw new Error("egress node response shape is invalid");
  return node;
};

const decodeEgressNodeListResilient: ApiDecoder<EgressNodeListDTO> = (value) => {
  if (!isObject(value)) throw new Error("egress node list response shape is invalid");
  const record = value as Record<string, unknown>;
  const itemsRaw = Array.isArray(record.items) ? record.items : [];
  const items = itemsRaw.map(normalizeEgressNode).filter((item): item is EgressNodeDTO => item !== null);
  return { items, defaultUserAgents: normalizeDefaultUserAgents(record.defaultUserAgents) };
};

const decodeEgressReportResilient: ApiDecoder<EgressReportDTO> = (value) => {
  if (!isObject(value)) throw new Error("egress report response shape is invalid");
  const record = value as Record<string, unknown>;
  const itemsRaw = Array.isArray(record.items) ? record.items : [];
  const items = itemsRaw.map(normalizeEgressNode).filter((item): item is EgressNodeDTO => item !== null);
  return {
    totalNodes: coerceNumber(record.totalNodes),
    enabledNodes: coerceNumber(record.enabledNodes),
    proxyNodes: coerceNumber(record.proxyNodes),
    healthyNodes: coerceNumber(record.healthyNodes),
    successCount: coerceNumber(record.successCount),
    failureCount: coerceNumber(record.failureCount),
    requestCount: coerceNumber(record.requestCount),
    successRate: coerceNumber(record.successRate),
    failureRate: coerceNumber(record.failureRate),
    items,
  };
};

const decodeEgressBatchImport: ApiDecoder<EgressBatchImportResultDTO> = (value) => {
  if (!isObject(value)) throw new Error("egress batch import response shape is invalid");
  const record = value as Record<string, unknown>;
  const itemsRaw = Array.isArray(record.items) ? record.items : [];
  const items = itemsRaw.map(normalizeEgressNode).filter((item): item is EgressNodeDTO => item !== null);
  const errors = Array.isArray(record.errors)
    ? record.errors.filter((item): item is string => typeof item === "string")
    : [];
  return {
    created: coerceNumber(record.created),
    failed: coerceNumber(record.failed),
    skipped: coerceNumber(record.skipped),
    errors,
    items,
  };
};

export type EgressReportDTO = {
  totalNodes: number; enabledNodes: number; proxyNodes: number; healthyNodes: number;
  successCount: number; failureCount: number; requestCount: number;
  successRate: number; failureRate: number; items: EgressNodeDTO[];
};

export type EgressProbeDTO = {
  nodeId: string; name: string; scope: EgressScope; ok: boolean;
  latencyMs: number; status?: number; error?: string; proxyUsed: boolean; checkedAt: string;
};

export type EgressProbeBatchDTO = {
  items: EgressProbeDTO[]; total: number; passed: number; failed: number;
};

export type SettingsSnapshotDTO = {
  config: SettingsConfigDTO;
  recommendedProviderBuild: { clientVersion: string; userAgent: string };
  updatedAt: string;
  revision: string;
  restartRequired: string[];
};

const settingsConfigValidator = hasShape({
  providerBuild: hasShape({ baseURL: isString, clientVersion: isString, clientIdentifier: isString, tokenAuth: isString, tokenAuthConfigured: isBoolean, userAgent: isString }),
  providerWeb: hasShape({
    baseURL: isString, quotaTimeout: isString, chatTimeout: isString, imageTimeout: isString, videoTimeout: isString,
    statsigMode: isOneOf("manual", "url"), statsigManualValue: isOptional(isString), statsigManualConfigured: isBoolean,
    statsigSignerURL: isString, mediaConcurrency: isNumber, allowNSFW: isBoolean, recoveryBackoffBase: isString, recoveryBackoffMax: isString,
  }),
  providerConsole: hasShape({ baseURL: isString, userAgent: isString, chatTimeout: isString }),
  proactiveUpstreamSync: hasShape({
    billing: isBoolean, webQuota: isBoolean, modelCatalogCatchup: isBoolean,
    allowManualBillingRefresh: isBoolean, allowManualQuotaRefresh: isBoolean,
  }),
  batch: hasShape({ importConcurrency: isNumber, conversionConcurrency: isNumber, syncConcurrency: isNumber, refreshConcurrency: isNumber, randomDelay: isString }),
  media: hasShape({ maxImageBytes: isNumber, maxTotalBytes: isNumber, cleanupThresholdPercent: isNumber, cleanupInterval: isString }),
  routing: hasShape({
    stickyTTL: isString, cooldownBase: isString, cooldownMax: isString, capacityWait: isString, maxAttempts: isNumber,
    retryStatusCodes: isArrayOf(isNumber), retryServerErrors: isBoolean,
  }),
  audit: hasShape({ bufferSize: isNumber, batchSize: isNumber, flushInterval: isString }),
  clientKeyDefaults: hasShape({ rpmLimit: isNumber, maxConcurrent: isNumber }),
});
const decodeSettingsSnapshot = createObjectDecoder<SettingsSnapshotDTO>("settings", {
  config: settingsConfigValidator,
  recommendedProviderBuild: hasShape({ clientVersion: isString, userAgent: isString }),
  updatedAt: isString,
  revision: isString,
  restartRequired: isArrayOf(isString),
});
// Keep strict-ish probe validators; list/report use resilient normalizers so one
// unexpected field (or historical scope typo) cannot blank the entire page.
const egressScopeValidator = isOneOf(...EGRESS_SCOPES);
const egressProbeValidator = hasShape({
  nodeId: isStringOrNumber, name: isString, scope: egressScopeValidator, ok: isBoolean,
  latencyMs: isNumber, status: isOptional(isNumber), error: isOptional(isString), proxyUsed: isBoolean, checkedAt: isString,
});
const decodeEgressProbe = createObjectDecoder<EgressProbeDTO>("egress probe", {
  nodeId: isStringOrNumber, name: isString, scope: egressScopeValidator, ok: isBoolean,
  latencyMs: isNumber, status: isOptional(isNumber), error: isOptional(isString), proxyUsed: isBoolean, checkedAt: isString,
});
const decodeEgressProbeBatch = createObjectDecoder<EgressProbeBatchDTO>("egress probe batch", {
  items: isArrayOf(egressProbeValidator), total: isNumber, passed: isNumber, failed: isNumber,
});

export function getSettings(): Promise<SettingsSnapshotDTO> {
  return apiRequest("/api/admin/v1/settings", {}, decodeSettingsSnapshot);
}

export function updateSettings(revision: string, config: SettingsConfigDTO): Promise<SettingsSnapshotDTO> {
  return apiRequest("/api/admin/v1/settings", { method: "PUT", body: { revision, config } }, decodeSettingsSnapshot);
}

export function listEgressNodes(input?: { sortBy?: string; sortOrder?: SortOrder; scope?: EgressScope }): Promise<EgressNodeListDTO> {
  const query = new URLSearchParams();
  if (input?.sortBy && input.sortOrder) {
    query.set("sortBy", input.sortBy);
    query.set("sortOrder", input.sortOrder);
  }
  if (input?.scope) query.set("scope", input.scope);
  const suffix = query.size > 0 ? `?${query}` : "";
  return apiRequest(`/api/admin/v1/egress-nodes${suffix}`, {}, decodeEgressNodeListResilient);
}

export function getEgressReport(scope?: EgressScope): Promise<EgressReportDTO> {
  const suffix = scope ? `?scope=${encodeURIComponent(scope)}` : "";
  return apiRequest(`/api/admin/v1/egress-nodes/report${suffix}`, {}, decodeEgressReportResilient);
}

export function testEgressNode(id: string): Promise<EgressProbeDTO> {
  return apiRequest(`/api/admin/v1/egress-nodes/${id}/test`, { method: "POST" }, decodeEgressProbe);
}

export function testAllEgressNodes(scope?: EgressScope): Promise<EgressProbeBatchDTO> {
  const suffix = scope ? `?scope=${encodeURIComponent(scope)}` : "";
  return apiRequest(`/api/admin/v1/egress-nodes/test${suffix}`, { method: "POST" }, decodeEgressProbeBatch);
}

export function createEgressNode(input: EgressNodeInput): Promise<EgressNodeDTO> {
  return apiRequest("/api/admin/v1/egress-nodes", {
    method: "POST",
    body: {
      ...input,
      scope: input.scopes[0] ?? input.scope,
      scopes: input.scopes,
    },
  }, decodeEgressNodeResilient);
}

export function createEgressNodesBatch(input: EgressBatchImportInput): Promise<EgressBatchImportResultDTO> {
  return apiRequest("/api/admin/v1/egress-nodes/batch", {
    method: "POST",
    body: {
      namePrefix: input.namePrefix,
      scope: input.scopes[0] ?? input.scope,
      scopes: input.scopes,
      enabled: input.enabled,
      proxyText: input.proxyText,
      userAgent: input.userAgent ?? "",
      cloudflareCookies: input.cloudflareCookies,
    },
  }, decodeEgressBatchImport);
}

export function updateEgressNode(id: string, input: EgressNodeInput): Promise<EgressNodeDTO> {
  return apiRequest(`/api/admin/v1/egress-nodes/${id}`, {
    method: "PUT",
    body: {
      ...input,
      scope: input.scopes[0] ?? input.scope,
      scopes: input.scopes,
    },
  }, decodeEgressNodeResilient);
}

export function deleteEgressNode(id: string): Promise<{ deleted: boolean }> {
  return apiRequest(`/api/admin/v1/egress-nodes/${id}`, { method: "DELETE" }, decodeBooleanResult<{ deleted: boolean }>("deleted"));
}

export function setEgressNodesEnabled(ids: string[], enabled: boolean): Promise<{ updated: number; enabled: boolean }> {
  return apiRequest("/api/admin/v1/egress-nodes/batch-enabled", {
    method: "POST",
    body: { ids, enabled },
  }, createObjectDecoder("egress batch enabled", { updated: isNumber, enabled: isBoolean }));
}

export function clearEgressNodesErrors(ids: string[]): Promise<{ cleared: number }> {
  return apiRequest("/api/admin/v1/egress-nodes/batch-clear-errors", {
    method: "POST",
    body: { ids },
  }, createObjectDecoder("egress batch clear errors", { cleared: isNumber }));
}
