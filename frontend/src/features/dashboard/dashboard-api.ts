import { apiRequest } from "@/shared/api/client";
import { createObjectDecoder, hasShape, isArrayOf, isNumber, isOneOf, isString } from "@/shared/api/decoder";
import type { PeriodValue } from "@/shared/lib/period";

export type DashboardPeriod = PeriodValue | "custom";

export type DashboardDTO = {
  period: DashboardPeriod;
  generatedAt: string;
  range: { start: string; end: string };
  resources: {
    activeAccounts: number;
    totalAccounts: number;
    enabledModels: number;
    totalModels: number;
    activeClientKeys: number;
    totalClientKeys: number;
    allTimeRequests: number;
  };
  usage: {
    requests: number;
    successfulRequests: number;
    failedRequests: number;
    inputTokens: number;
    cachedInputTokens: number;
    outputTokens: number;
    reasoningTokens: number;
    tokens: number;
    billedCostUsdTicks: number;
    successRate: number;
  };
  /** Site-wide rates over the last ~60s (new-api style). */
  liveRates: { rpm: number; tpm: number; windowSeconds: number };
  /** Calendar-day totals in the admin timezone. */
  today: { requests: number; tokens: number; start: string; end: string };
  series: Array<{ start: string; end: string; requests: number; inputTokens: number; cachedInputTokens: number; outputTokens: number; reasoningTokens: number; tokens: number; billedCostUsdTicks: number; models: Array<{ model: string; tokens: number; billedCostUsdTicks: number }> }>;
  topModels: Array<{ model: string; requests: number; inputTokens: number; cachedInputTokens: number; outputTokens: number; reasoningTokens: number; tokens: number; billedCostUsdTicks: number }>;
};

const dashboardSeriesModel = hasShape({ model: isString, tokens: isNumber, billedCostUsdTicks: isNumber });
const dashboardSeriesItem = hasShape({
  start: isString, end: isString, requests: isNumber, inputTokens: isNumber, cachedInputTokens: isNumber,
  outputTokens: isNumber, reasoningTokens: isNumber, tokens: isNumber, billedCostUsdTicks: isNumber, models: isArrayOf(dashboardSeriesModel),
});
const dashboardModelItem = hasShape({
  model: isString, requests: isNumber, inputTokens: isNumber, cachedInputTokens: isNumber,
  outputTokens: isNumber, reasoningTokens: isNumber, tokens: isNumber, billedCostUsdTicks: isNumber,
});
const decodeDashboard = createObjectDecoder<DashboardDTO>("dashboard", {
  period: isOneOf("24h", "7d", "30d", "90d", "custom"),
  generatedAt: isString,
  range: hasShape({ start: isString, end: isString }),
  resources: hasShape({
    activeAccounts: isNumber, totalAccounts: isNumber, enabledModels: isNumber, totalModels: isNumber,
    activeClientKeys: isNumber, totalClientKeys: isNumber, allTimeRequests: isNumber,
  }),
  usage: hasShape({
    requests: isNumber, successfulRequests: isNumber, failedRequests: isNumber, inputTokens: isNumber,
    cachedInputTokens: isNumber, outputTokens: isNumber, reasoningTokens: isNumber, tokens: isNumber,
    billedCostUsdTicks: isNumber, successRate: isNumber,
  }),
  liveRates: hasShape({ rpm: isNumber, tpm: isNumber, windowSeconds: isNumber }),
  today: hasShape({ requests: isNumber, tokens: isNumber, start: isString, end: isString }),
  series: isArrayOf(dashboardSeriesItem),
  topModels: isArrayOf(dashboardModelItem),
});

export type DashboardQuery = {
  period: DashboardPeriod;
  timezone: string;
  refresh?: boolean;
  /** RFC3339 or YYYY-MM-DD when period=custom */
  start?: string;
  end?: string;
};

export function getDashboard(input: DashboardQuery): Promise<DashboardDTO> {
  const query = new URLSearchParams({ period: input.period, timezone: input.timezone });
  if (input.refresh) query.set("refresh", "1");
  if (input.period === "custom") {
    if (input.start) query.set("start", input.start);
    if (input.end) query.set("end", input.end);
  }
  return apiRequest(`/api/admin/v1/dashboard?${query.toString()}`, {}, decodeDashboard);
}
