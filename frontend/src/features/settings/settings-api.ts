import { apiRequest } from "@/shared/api/client";
import { createObjectDecoder, decodeBooleanResult, hasShape, isArrayOf, isBoolean, isNumber, isOneOf, isOptional, isString } from "@/shared/api/decoder";
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
  id: string; name: string; scope: EgressScope; enabled: boolean;
  proxyConfigured: boolean; userAgent: string; cookieConfigured: boolean;
  health: number; failureCount: number; cooldownUntil?: string; lastError?: string;
  successCount: number; requestCount: number; successRate: number; failureRate: number;
  inflight: number; lastProbeAt?: string; lastProbeOK?: boolean; lastProbeMs?: number; lastProbeError?: string;
};

export type EgressNodeInput = {
  name: string; scope: EgressScope; enabled: boolean; proxyURL?: string;
  clearProxyURL?: boolean; userAgent: string; cloudflareCookies?: string; clearCookies?: boolean;
};

export type EgressScope = "grok_build" | "grok_web" | "grok_console" | "grok_web_asset";
export type EgressNodeListDTO = { items: EgressNodeDTO[]; defaultUserAgents: Record<EgressScope, string> };

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
const egressScopeValidator = isOneOf("grok_build", "grok_web", "grok_console", "grok_web_asset");
const egressNodeValidator = hasShape({
  id: isString, name: isString, scope: egressScopeValidator, enabled: isBoolean,
  proxyConfigured: isBoolean, userAgent: isString, cookieConfigured: isBoolean, health: isNumber, failureCount: isNumber,
  cooldownUntil: isOptional(isString), lastError: isOptional(isString),
  successCount: isNumber, requestCount: isNumber, successRate: isNumber, failureRate: isNumber, inflight: isNumber,
  lastProbeAt: isOptional(isString), lastProbeOK: isOptional(isBoolean), lastProbeMs: isOptional(isNumber), lastProbeError: isOptional(isString),
});
const decodeEgressNode = createObjectDecoder<EgressNodeDTO>("egress node", {
  id: isString, name: isString, scope: egressScopeValidator, enabled: isBoolean,
  proxyConfigured: isBoolean, userAgent: isString, cookieConfigured: isBoolean, health: isNumber, failureCount: isNumber,
  cooldownUntil: isOptional(isString), lastError: isOptional(isString),
  successCount: isNumber, requestCount: isNumber, successRate: isNumber, failureRate: isNumber, inflight: isNumber,
  lastProbeAt: isOptional(isString), lastProbeOK: isOptional(isBoolean), lastProbeMs: isOptional(isNumber), lastProbeError: isOptional(isString),
});
const decodeEgressNodeList = createObjectDecoder<EgressNodeListDTO>("egress node list", {
  items: isArrayOf(egressNodeValidator),
  defaultUserAgents: hasShape({ grok_build: isString, grok_web: isString, grok_console: isString, grok_web_asset: isString }),
});
const decodeEgressReport = createObjectDecoder<EgressReportDTO>("egress report", {
  totalNodes: isNumber, enabledNodes: isNumber, proxyNodes: isNumber, healthyNodes: isNumber,
  successCount: isNumber, failureCount: isNumber, requestCount: isNumber, successRate: isNumber, failureRate: isNumber,
  items: isArrayOf(egressNodeValidator),
});
const egressProbeValidator = hasShape({
  nodeId: isString, name: isString, scope: egressScopeValidator, ok: isBoolean,
  latencyMs: isNumber, status: isOptional(isNumber), error: isOptional(isString), proxyUsed: isBoolean, checkedAt: isString,
});
const decodeEgressProbe = createObjectDecoder<EgressProbeDTO>("egress probe", {
  nodeId: isString, name: isString, scope: egressScopeValidator, ok: isBoolean,
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
  return apiRequest(`/api/admin/v1/egress-nodes${suffix}`, {}, decodeEgressNodeList);
}

export function getEgressReport(scope?: EgressScope): Promise<EgressReportDTO> {
  const suffix = scope ? `?scope=${encodeURIComponent(scope)}` : "";
  return apiRequest(`/api/admin/v1/egress-nodes/report${suffix}`, {}, decodeEgressReport);
}

export function testEgressNode(id: string): Promise<EgressProbeDTO> {
  return apiRequest(`/api/admin/v1/egress-nodes/${id}/test`, { method: "POST" }, decodeEgressProbe);
}

export function testAllEgressNodes(scope?: EgressScope): Promise<EgressProbeBatchDTO> {
  const suffix = scope ? `?scope=${encodeURIComponent(scope)}` : "";
  return apiRequest(`/api/admin/v1/egress-nodes/test${suffix}`, { method: "POST" }, decodeEgressProbeBatch);
}

export function createEgressNode(input: EgressNodeInput): Promise<EgressNodeDTO> {
  return apiRequest("/api/admin/v1/egress-nodes", { method: "POST", body: input }, decodeEgressNode);
}

export function updateEgressNode(id: string, input: EgressNodeInput): Promise<EgressNodeDTO> {
  return apiRequest(`/api/admin/v1/egress-nodes/${id}`, { method: "PUT", body: input }, decodeEgressNode);
}

export function deleteEgressNode(id: string): Promise<{ deleted: boolean }> {
  return apiRequest(`/api/admin/v1/egress-nodes/${id}`, { method: "DELETE" }, decodeBooleanResult<{ deleted: boolean }>("deleted"));
}
