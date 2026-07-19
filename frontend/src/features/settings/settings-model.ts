import { z } from "zod";

import type { SettingsConfigDTO } from "@/features/settings/settings-api";

export type DurationUnit = "s" | "m" | "h" | "d";
export type DurationValue = { value: number; unit: DurationUnit };
export type ByteSizeUnit = "MiB" | "GiB";
export type ByteSizeValue = { value: number; unit: ByteSizeUnit };

// Keep number input/output types identical for zodResolver + RHF (z.coerce.number
// widens input to unknown and breaks tsc under TS 6 + zod 4).
// Empty/cleared inputs become NaN via valueAsNumber and fail the finite check.
const durationSchema = z.object({
  value: z.number().refine((value) => Number.isFinite(value) && value > 0, { message: "invalid" }),
  unit: z.enum(["s", "m", "h", "d"]),
});
const positiveInteger = z.number().int().positive();
const byteSizeSchema = z.object({
  value: z.number().refine((value) => Number.isFinite(value) && value > 0, { message: "invalid" }),
  unit: z.enum(["MiB", "GiB"]),
});
const routingTTLDuration = durationSchema.refine((value) => durationSeconds(value) <= 30 * 86_400);
const routingCooldownDuration = durationSchema.refine((value) => durationSeconds(value) <= 86_400);
const routingCapacityWaitDuration = durationSchema.refine((value) => durationSeconds(value) <= 5);
const auditFlushDuration = durationSchema.refine((value) => {
  const seconds = durationSeconds(value);
  return seconds >= 0.01 && seconds <= 60;
});
const consoleChatDuration = durationSchema.refine((value) => {
  const seconds = durationSeconds(value);
  return seconds >= 5 && seconds <= 30 * 60;
});

export const settingsSchema = z.object({
  providerBuild: z.object({
    baseURL: z.url(),
    clientVersion: z.string().trim().min(1),
    clientIdentifier: z.string().trim().min(1),
    tokenAuth: z.string().trim(),
    tokenAuthConfigured: z.boolean(),
    userAgent: z.string().trim().min(1),
  }).superRefine((value, context) => {
    if (!value.tokenAuthConfigured && value.tokenAuth.length === 0) {
      context.addIssue({ code: "custom", path: ["tokenAuth"], message: "required" });
    }
  }),
  providerWeb: z.object({
    baseURL: z.url().refine((value) => value.startsWith("https://")),
    statsigMode: z.enum(["local", "manual", "url"]),
    statsigManualValue: z.string().trim().max(4096),
    statsigManualConfigured: z.boolean(),
    statsigSignerURL: z.string().trim().max(2048),
    quotaTimeout: durationSchema, chatTimeout: durationSchema, imageTimeout: durationSchema, videoTimeout: durationSchema,
    mediaConcurrency: positiveInteger.max(64), allowNSFW: z.boolean(),
    recoveryBackoffBase: durationSchema, recoveryBackoffMax: durationSchema,
    flareSolverrEnabled: z.boolean(),
    flareSolverrURL: z.string().trim().max(2048),
    flareSolverrTargetURL: z.string().trim().max(2048),
    flareSolverrTimeout: durationSchema,
    flareSolverrRefreshInterval: durationSchema,
  }).superRefine((value, context) => {
    if (durationSeconds(value.recoveryBackoffMax) < durationSeconds(value.recoveryBackoffBase)) {
      context.addIssue({ code: "custom", path: ["recoveryBackoffMax"], message: "invalid" });
    }
    if (value.flareSolverrEnabled) {
      const url = value.flareSolverrURL.trim();
      if (!url.startsWith("http://") && !url.startsWith("https://")) {
        context.addIssue({ code: "custom", path: ["flareSolverrURL"], message: "invalid" });
      }
    }
    if (value.statsigMode === "manual" && !value.statsigManualConfigured && value.statsigManualValue.length === 0) {
      context.addIssue({ code: "custom", path: ["statsigManualValue"], message: "required" });
    }
    if (value.statsigManualValue.length > 0 && !validStatsigID(value.statsigManualValue)) {
      context.addIssue({ code: "custom", path: ["statsigManualValue"], message: "invalid" });
    }
    if (value.statsigMode === "url") {
      if (!validStatsigSignerURL(value.statsigSignerURL)) {
        context.addIssue({ code: "custom", path: ["statsigSignerURL"], message: "invalid" });
      }
    }
  }),
  providerConsole: z.object({
    baseURL: z.url().refine((value) => value.startsWith("https://")),
    userAgent: z.string().trim().min(1).max(512),
    chatTimeout: consoleChatDuration,
  }),
  proactiveUpstreamSync: z.object({
    billing: z.boolean(),
    webQuota: z.boolean(),
    modelCatalogCatchup: z.boolean(),
    allowManualBillingRefresh: z.boolean(),
    allowManualQuotaRefresh: z.boolean(),
  }),
  batch: z.object({
    importConcurrency: positiveInteger.max(50),
    conversionConcurrency: positiveInteger.max(50),
    syncConcurrency: positiveInteger.max(50),
    refreshConcurrency: positiveInteger.max(50),
    randomDelay: z.number().int().min(0).max(5_000),
    dbBuffer: z.object({
      enabled: z.boolean(),
      driver: z.enum(["none", "redis", "sqlite"]),
      path: z.string().optional(),
    }),
  }),
  media: z.object({
    maxImageSize: byteSizeSchema.refine((value) => byteSizeBytes(value) >= 1 << 20 && byteSizeBytes(value) <= 32 << 20),
    maxTotalSize: byteSizeSchema.refine((value) => byteSizeBytes(value) <= 2 ** 40),
    cleanupThresholdPercent: z.number().int().min(50).max(95),
    cleanupInterval: durationSchema.refine((value) => durationSeconds(value) >= 60 && durationSeconds(value) <= 86_400),
  }).refine((value) => byteSizeBytes(value.maxTotalSize) >= byteSizeBytes(value.maxImageSize), { path: ["maxTotalSize"] }),
  routing: z.object({
    stickyTTL: routingTTLDuration,
    cooldownBase: routingCooldownDuration,
    cooldownMax: routingCooldownDuration,
    capacityWait: routingCapacityWaitDuration,
    maxAttempts: positiveInteger.max(10),
    retryStatusCodesText: z.string().trim().min(1).superRefine((value, context) => {
      const codes = parseStatusCodeList(value);
      if (codes === null) {
        context.addIssue({ code: "custom", message: "invalid" });
      }
    }),
    retryServerErrors: z.boolean(),
    deprioritizeFailedAccounts: z.boolean(),
  }).refine((value) => durationSeconds(value.cooldownMax) >= durationSeconds(value.cooldownBase), { path: ["cooldownMax"] }),
  promptCacheAffinity: z.object({
    enabled: z.boolean(),
    fingerprint: z.boolean(),
    expire: z.boolean(),
    ttl: durationSchema.refine((value) => durationSeconds(value) >= 60 && durationSeconds(value) <= 30 * 24 * 3600),
  }),
  audit: z.object({ bufferSize: positiveInteger.max(262_144), batchSize: positiveInteger.max(4_096), flushInterval: auditFlushDuration })
    .refine((value) => value.batchSize <= value.bufferSize, { path: ["batchSize"] }),
  clientKeyDefaults: z.object({ rpmLimit: positiveInteger.max(100_000), maxConcurrent: positiveInteger.max(1_024) }),
});

export type SettingsForm = z.infer<typeof settingsSchema>;

export function toSettingsForm(config: SettingsConfigDTO): SettingsForm {
  return {
    providerBuild: { ...config.providerBuild, tokenAuth: "" },
    providerWeb: {
      ...config.providerWeb,
      statsigManualValue: "",
      quotaTimeout: parseDuration(config.providerWeb.quotaTimeout), chatTimeout: parseDuration(config.providerWeb.chatTimeout),
      imageTimeout: parseDuration(config.providerWeb.imageTimeout), videoTimeout: parseDuration(config.providerWeb.videoTimeout),
      recoveryBackoffBase: parseDuration(config.providerWeb.recoveryBackoffBase), recoveryBackoffMax: parseDuration(config.providerWeb.recoveryBackoffMax),
      flareSolverrEnabled: config.providerWeb.flareSolverrEnabled ?? false,
      flareSolverrURL: config.providerWeb.flareSolverrURL ?? "",
      flareSolverrTargetURL: config.providerWeb.flareSolverrTargetURL || "https://grok.com/",
      flareSolverrTimeout: parseDuration(config.providerWeb.flareSolverrTimeout ?? "60s"),
      flareSolverrRefreshInterval: parseDuration(config.providerWeb.flareSolverrRefreshInterval ?? "1h"),
    },
    providerConsole: { ...config.providerConsole, chatTimeout: parseDuration(config.providerConsole.chatTimeout) },
    proactiveUpstreamSync: {
      billing: config.proactiveUpstreamSync?.billing ?? false,
      webQuota: config.proactiveUpstreamSync?.webQuota ?? false,
      modelCatalogCatchup: config.proactiveUpstreamSync?.modelCatalogCatchup ?? false,
      allowManualBillingRefresh: config.proactiveUpstreamSync?.allowManualBillingRefresh ?? false,
      allowManualQuotaRefresh: config.proactiveUpstreamSync?.allowManualQuotaRefresh ?? false,
    },
    batch: {
      ...config.batch,
      randomDelay: parseDurationMilliseconds(config.batch.randomDelay),
      dbBuffer: {
        enabled: Boolean(config.batch.dbBuffer?.enabled),
        driver: (config.batch.dbBuffer?.driver === "redis" || config.batch.dbBuffer?.driver === "sqlite" || config.batch.dbBuffer?.driver === "none")
          ? config.batch.dbBuffer.driver
          : "none" as const,
        path: config.batch.dbBuffer?.path ?? "",
      },
    },
    media: {
      maxImageSize: parseByteSize(config.media.maxImageBytes), maxTotalSize: parseByteSize(config.media.maxTotalBytes),
      cleanupThresholdPercent: config.media.cleanupThresholdPercent,
      cleanupInterval: parseDuration(config.media.cleanupInterval),
    },
    routing: {
      stickyTTL: parseDuration(config.routing.stickyTTL), cooldownBase: parseDuration(config.routing.cooldownBase),
      cooldownMax: parseDuration(config.routing.cooldownMax), capacityWait: parseDuration(config.routing.capacityWait), maxAttempts: config.routing.maxAttempts,
      retryStatusCodesText: formatStatusCodeList(config.routing.retryStatusCodes ?? [402, 403, 429, 503]),
      retryServerErrors: config.routing.retryServerErrors ?? true,
      deprioritizeFailedAccounts: config.routing.deprioritizeFailedAccounts ?? true,
    },
    promptCacheAffinity: {
      enabled: config.promptCacheAffinity?.enabled ?? true,
      fingerprint: config.promptCacheAffinity?.fingerprint ?? true,
      expire: config.promptCacheAffinity?.expire ?? true,
      ttl: parseDuration(config.promptCacheAffinity?.ttl ?? "24h"),
    },
    audit: { bufferSize: config.audit.bufferSize, batchSize: config.audit.batchSize, flushInterval: parseDuration(config.audit.flushInterval) },
    clientKeyDefaults: config.clientKeyDefaults,
  };
}

export function toSettingsDTO(config: SettingsForm): SettingsConfigDTO {
  return {
    providerBuild: config.providerBuild,
    providerWeb: {
      ...config.providerWeb,
      quotaTimeout: formatDuration(config.providerWeb.quotaTimeout), chatTimeout: formatDuration(config.providerWeb.chatTimeout),
      imageTimeout: formatDuration(config.providerWeb.imageTimeout), videoTimeout: formatDuration(config.providerWeb.videoTimeout),
      recoveryBackoffBase: formatDuration(config.providerWeb.recoveryBackoffBase), recoveryBackoffMax: formatDuration(config.providerWeb.recoveryBackoffMax),
      flareSolverrEnabled: config.providerWeb.flareSolverrEnabled,
      flareSolverrURL: config.providerWeb.flareSolverrURL,
      flareSolverrTargetURL: config.providerWeb.flareSolverrTargetURL,
      flareSolverrTimeout: formatDuration(config.providerWeb.flareSolverrTimeout),
      flareSolverrRefreshInterval: formatDuration(config.providerWeb.flareSolverrRefreshInterval),
    },
    providerConsole: { ...config.providerConsole, chatTimeout: formatDuration(config.providerConsole.chatTimeout) },
    proactiveUpstreamSync: config.proactiveUpstreamSync,
    batch: (() => {
      const dbBuffer = { ...config.batch.dbBuffer };
      if (dbBuffer.enabled && (dbBuffer.driver === "none" || !dbBuffer.driver)) {
        dbBuffer.enabled = false;
        dbBuffer.driver = "none";
      }
      if (dbBuffer.enabled && dbBuffer.driver === "sqlite" && !(dbBuffer.path ?? "").trim()) {
        dbBuffer.enabled = false;
      }
      return { ...config.batch, randomDelay: `${config.batch.randomDelay}ms`, dbBuffer };
    })(),
    media: {
      maxImageBytes: byteSizeBytes(config.media.maxImageSize), maxTotalBytes: byteSizeBytes(config.media.maxTotalSize),
      cleanupThresholdPercent: config.media.cleanupThresholdPercent,
      cleanupInterval: formatDuration(config.media.cleanupInterval),
    },
    routing: {
      stickyTTL: formatDuration(config.routing.stickyTTL), cooldownBase: formatDuration(config.routing.cooldownBase),
      cooldownMax: formatDuration(config.routing.cooldownMax), capacityWait: formatDuration(config.routing.capacityWait), maxAttempts: config.routing.maxAttempts,
      retryStatusCodes: parseStatusCodeList(config.routing.retryStatusCodesText) ?? [402, 403, 429, 503],
      retryServerErrors: config.routing.retryServerErrors,
      deprioritizeFailedAccounts: config.routing.deprioritizeFailedAccounts,
    },
    promptCacheAffinity: {
      enabled: config.promptCacheAffinity.enabled,
      fingerprint: config.promptCacheAffinity.fingerprint,
      expire: config.promptCacheAffinity.expire,
      ttl: formatDuration(config.promptCacheAffinity.ttl),
    },
    audit: { bufferSize: config.audit.bufferSize, batchSize: config.audit.batchSize, flushInterval: formatDuration(config.audit.flushInterval) },
    clientKeyDefaults: config.clientKeyDefaults,
  };
}

export function isDurationUnit(value: string): value is DurationUnit {
  return value === "s" || value === "m" || value === "h" || value === "d";
}

export function isByteSizeUnit(value: string): value is ByteSizeUnit {
  return value === "MiB" || value === "GiB";
}

function byteSizeBytes(value: ByteSizeValue): number {
  return Math.round(value.value * (value.unit === "GiB" ? 2 ** 30 : 2 ** 20));
}

function parseByteSize(bytes: number): ByteSizeValue {
  if (bytes >= 2 ** 30 && bytes % 2 ** 30 === 0) return { value: bytes / 2 ** 30, unit: "GiB" };
  return { value: bytes / 2 ** 20, unit: "MiB" };
}

function durationSeconds(value: DurationValue): number {
  const factors: Record<DurationUnit, number> = { s: 1, m: 60, h: 3_600, d: 86_400 };
  return value.value * factors[value.unit];
}

function formatDuration(value: DurationValue): string {
  if (value.unit === "d") return `${value.value * 24}h`;
  return `${value.value}${value.unit}`;
}

function parseDuration(value: string): DurationValue {
  const simple = value.match(/^(\d+(?:\.\d+)?)(ms|s|m|h)$/);
  if (simple) {
    const amount = Number(simple[1]);
    if (simple[2] === "ms") return { value: amount / 1000, unit: "s" };
    if (simple[2] === "h" && amount >= 24 && amount % 24 === 0) return { value: amount / 24, unit: "d" };
    if (isDurationUnit(simple[2])) return { value: amount, unit: simple[2] };
  }

  const factors: Record<string, number> = { ns: 0.000001, us: 0.001, "µs": 0.001, ms: 1, s: 1000, m: 60_000, h: 3_600_000 };
  const parts = [...value.matchAll(/(\d+(?:\.\d+)?)(ns|us|µs|ms|s|m|h)/g)];
  if (parts.map((part) => part[0]).join("") !== value || parts.length === 0) return { value: 1, unit: "s" };
  const milliseconds = parts.reduce((total, part) => total + Number(part[1]) * factors[part[2]], 0);
  const units: Array<[DurationUnit, number]> = [["d", 86_400_000], ["h", 3_600_000], ["m", 60_000], ["s", 1000]];
  for (const [unit, factor] of units) {
    const amount = milliseconds / factor;
    if (amount >= 1 && Number.isInteger(amount)) return { value: amount, unit };
  }
  return { value: milliseconds / 1000, unit: "s" };
}

function parseDurationMilliseconds(value: string): number {
  return Math.round(durationSeconds(parseDuration(value)) * 1000);
}

function formatStatusCodeList(codes: number[]): string {
  return codes.join(", ");
}

function parseStatusCodeList(value: string): number[] | null {
  const parts = value.split(/[\s,;]+/).map((part) => part.trim()).filter(Boolean);
  if (parts.length === 0) return null;
  const codes: number[] = [];
  const seen = new Set<number>();
  for (const part of parts) {
    if (!/^\d{3}$/.test(part)) return null;
    const code = Number(part);
    if (!Number.isInteger(code) || code < 100 || code > 599) return null;
    if (seen.has(code)) continue;
    seen.add(code);
    codes.push(code);
  }
  return codes;
}

function validStatsigID(value: string): boolean {
  try {
    const normalized = value.trim().replace(/-/g, "+").replace(/_/g, "/");
    const padded = normalized.padEnd(Math.ceil(normalized.length / 4) * 4, "=");
    return atob(padded).length === 70;
  } catch {
    return false;
  }
}

function validStatsigSignerURL(value: string): boolean {
  try {
    const parsed = new URL(value);
    if (parsed.username !== "" || parsed.password !== "" || parsed.search !== "" || parsed.hash !== "") return false;
    const internal = internalSignerHostname(parsed.hostname);
    if (internal) return parsed.protocol === "http:" || parsed.protocol === "https:";
    return parsed.protocol === "https:" && (parsed.port === "" || parsed.port === "443");
  } catch {
    return false;
  }
}

function internalSignerHostname(value: string): boolean {
  const host = value.toLowerCase().replace(/^\[|\]$/g, "").replace(/\.$/, "");
  if (host === "localhost" || host.endsWith(".localhost") || host.endsWith(".local") || host.endsWith(".internal")) return true;
  if (!host.includes(".")) {
    if (host.includes(":")) return host === "::1" || /^(?:fc|fd|fe[89ab])/i.test(host);
    return /^[a-z0-9](?:[a-z0-9_-]{0,61}[a-z0-9])?$/i.test(host);
  }
  const octets = host.split(".").map(Number);
  if (octets.length !== 4 || octets.some((part) => !Number.isInteger(part) || part < 0 || part > 255)) return false;
  return octets[0] === 10 || octets[0] === 127 || octets[0] === 169 && octets[1] === 254 || octets[0] === 172 && octets[1] >= 16 && octets[1] <= 31 || octets[0] === 192 && octets[1] === 168;
}
