import { zodResolver } from "@hookform/resolvers/zod";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect } from "react";
import { useForm, type FieldErrors, type Resolver, type SubmitHandler, type SubmitErrorHandler } from "react-hook-form";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";

import { getSettings, updateSettings } from "@/features/settings/settings-api";
import { settingsSchema, toSettingsDTO, toSettingsForm, type SettingsForm } from "@/features/settings/settings-model";

function collectErrorPaths(errors: FieldErrors, prefix = ""): string[] {
  const paths: string[] = [];
  for (const [key, value] of Object.entries(errors)) {
    if (!value) continue;
    const path = prefix ? `${prefix}.${key}` : key;
    if (typeof value === "object" && value !== null && "message" in value && value.message) {
      paths.push(path);
      continue;
    }
    if (typeof value === "object" && value !== null) {
      paths.push(...collectErrorPaths(value as FieldErrors, path));
    }
  }
  return paths;
}

export function useSettings() {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const settingsQuery = useQuery({ queryKey: ["settings"], queryFn: getSettings });
  const form = useForm<SettingsForm>({
    // Cast: zod effects/refines can widen resolver generics under TS 6 + zod 4.
    resolver: zodResolver(settingsSchema) as Resolver<SettingsForm>,
    mode: "onSubmit",
    reValidateMode: "onChange",
    shouldFocusError: true,
  });
  const updateMutation = useMutation({
    mutationFn: async (config: SettingsForm) => {
      try {
        return await updateSettings(settingsQuery.data?.revision ?? "0", toSettingsDTO(config));
      } catch (error) {
        throw error instanceof Error ? error : new Error(String(error));
      }
    },
    onSuccess: (snapshot) => {
      queryClient.setQueryData(["settings"], snapshot);
      void queryClient.invalidateQueries({ queryKey: ["system-info"] });
      form.reset(toSettingsForm(snapshot.config));
      toast.success(t("settings.saved"));
    },
    onError: (error) => toast.error(error instanceof Error ? error.message : t("errors.generic")),
  });

  useEffect(() => {
    if (settingsQuery.data) {
      form.reset(toSettingsForm(settingsQuery.data.config), { keepDefaultValues: false });
    }
  }, [form, settingsQuery.data]);

  const onValid: SubmitHandler<SettingsForm> = (values) => {
    updateMutation.mutate(values);
  };

  const onInvalid: SubmitErrorHandler<SettingsForm> = (errors) => {
    const paths = collectErrorPaths(errors);
    const preview = paths.slice(0, 4).join(", ");
    const more = paths.length > 4 ? ` (+${paths.length - 4})` : "";
    toast.error(
      paths.length > 0
        ? t("settings.validationFailed", { fields: `${preview}${more}`, defaultValue: `校验失败：${preview}${more}` })
        : t("settings.validationFailedGeneric", { defaultValue: "表单校验未通过，请检查标红字段" }),
    );
    const first = paths[0];
    if (first) {
      try {
        form.setFocus(first as never);
      } catch {
        // nested duration objects may not be focusable
      }
    }
  };

  return {
    form,
    settingsQuery,
    updateMutation,
    onValid,
    onInvalid,
    reset: () => { if (settingsQuery.data) form.reset(toSettingsForm(settingsQuery.data.config)); },
  };
}
