import { Button } from "@/components/ui/button";
import { cn } from "@/shared/lib/cn";
import { PERIOD_DAYS, toPeriodValue, type PeriodDays, type PeriodSelection } from "@/shared/lib/period";

type BaseProps = {
  ariaLabel: string;
  className?: string;
};

type FixedPeriodProps = BaseProps & {
  allowCustom?: false;
  value: PeriodDays;
  onChange: (value: PeriodDays) => void;
};

type CustomPeriodProps = BaseProps & {
  allowCustom: true;
  value: PeriodSelection;
  onChange: (value: PeriodSelection) => void;
  customLabel?: string;
};

export function PeriodSelector(props: FixedPeriodProps | CustomPeriodProps) {
  const { value, ariaLabel, className } = props;
  const allowCustom = props.allowCustom === true;
  const customLabel = allowCustom ? (props.customLabel ?? "custom") : "custom";

  function selectPreset(days: PeriodDays): void {
    props.onChange(days);
  }

  function selectCustom(): void {
    if (props.allowCustom) {
      props.onChange("custom");
    }
  }

  return (
    <div className={cn("inline-flex h-8 items-center gap-0.5 rounded-md bg-muted p-0.5", className)} role="group" aria-label={ariaLabel}>
      {PERIOD_DAYS.map((days) => (
        <Button
          key={days}
          type="button"
          variant="ghost"
          size="sm"
          className={cn("h-7 min-w-9 rounded-sm px-1.5 text-xs font-normal sm:min-w-11 sm:px-2", value === days && "bg-background shadow-sm hover:bg-background")}
          aria-pressed={value === days}
          onClick={() => selectPreset(days)}
        >
          {toPeriodValue(days)}
        </Button>
      ))}
      {allowCustom ? (
        <Button
          type="button"
          variant="ghost"
          size="sm"
          className={cn("h-7 min-w-9 rounded-sm px-1.5 text-xs font-normal sm:min-w-11 sm:px-2", value === "custom" && "bg-background shadow-sm hover:bg-background")}
          aria-pressed={value === "custom"}
          onClick={selectCustom}
        >
          {customLabel}
        </Button>
      ) : null}
    </div>
  );
}
