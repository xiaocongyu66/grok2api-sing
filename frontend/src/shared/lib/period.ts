export const PERIOD_DAYS = [1, 7, 30, 90] as const;

export type PeriodDays = (typeof PERIOD_DAYS)[number];
export type PeriodValue = "24h" | "7d" | "30d" | "90d";
export type PeriodSelection = PeriodDays | "custom";

export const CUSTOM_RANGE_MIN = "2009-01-01";
export const CUSTOM_RANGE_MAX = "2030-12-31";

export function toPeriodValue(days: PeriodDays): PeriodValue {
  return days === 1 ? "24h" : `${days}d`;
}

/** Local calendar date as YYYY-MM-DD. */
export function formatDateInput(date: Date): string {
  const year = date.getFullYear();
  const month = String(date.getMonth() + 1).padStart(2, "0");
  const day = String(date.getDate()).padStart(2, "0");
  return `${year}-${month}-${day}`;
}

export function clampDateInput(value: string, min = CUSTOM_RANGE_MIN, max = CUSTOM_RANGE_MAX): string {
  if (!value) return value;
  if (value < min) return min;
  if (value > max) return max;
  return value;
}
