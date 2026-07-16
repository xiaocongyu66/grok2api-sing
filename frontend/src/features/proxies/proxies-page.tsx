import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Activity, CheckCircle2, Dices, Eraser, FileUp, MoreHorizontal, Pencil, Plus, Power, RefreshCw, Trash2, XCircle, Zap } from "lucide-react";
import { type ReactNode, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuSeparator, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Spinner } from "@/components/ui/spinner";
import { Switch } from "@/components/ui/switch";
import { Table, TableActionCell, TableActionHead, TableBody, TableCell, TableHeader, TableRow } from "@/components/ui/table";
import { Textarea } from "@/components/ui/textarea";
import {
  clearEgressNodesErrors,
  createEgressNode,
  createEgressNodesBatch,
  deleteEgressNode,
  EGRESS_SCOPES,
  getEgressReport,
  listEgressNodes,
  setEgressNodesEnabled,
  testAllEgressNodes,
  testEgressNode,
  updateEgressNode,
  type EgressNodeDTO,
  type EgressNodeInput,
  type EgressScope,
} from "@/features/settings/settings-api";
import { ErrorState } from "@/shared/components/data-state";
import { SortableTableHead } from "@/shared/components/sortable-table-head";
import { cn } from "@/shared/lib/cn";
import { formatDateTime } from "@/shared/lib/format";
import { nextTableSort, type SortOrder, type TableSort } from "@/shared/lib/table-sort";

const emptyInput: EgressNodeInput = {
  name: "",
  scope: "grok_build",
  scopes: ["grok_build"],
  enabled: true,
  proxyURL: "",
  userAgent: "",
  cloudflareCookies: "",
};

type BatchImportForm = {
  namePrefix: string;
  scopes: EgressScope[];
  enabled: boolean;
  proxyText: string;
  userAgent: string;
  cloudflareCookies: string;
};

const emptyBatch: BatchImportForm = {
  namePrefix: "",
  scopes: ["grok_build"],
  enabled: true,
  proxyText: "",
  userAgent: "",
  cloudflareCookies: "",
};

/** Browser UA samples for the “随机” button (fill once into the input). */
const BROWSER_UA_POOL = [
  "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/146.0.0.0 Safari/537.36",
  "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/145.0.0.0 Safari/537.36",
  "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/146.0.0.0 Safari/537.36",
  "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/145.0.0.0 Safari/537.36",
  "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/146.0.0.0 Safari/537.36",
  "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/146.0.0.0 Safari/537.36 Edg/146.0.0.0",
  "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/146.0.0.0 Safari/537.36 Edg/146.0.0.0",
  "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:136.0) Gecko/20100101 Firefox/136.0",
  "Mozilla/5.0 (Macintosh; Intel Mac OS X 10.15; rv:136.0) Gecko/20100101 Firefox/136.0",
  "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.3 Safari/605.1.15",
];

function pickRandomUserAgent(current = ""): string {
  if (BROWSER_UA_POOL.length === 0) return current;
  if (BROWSER_UA_POOL.length === 1) return BROWSER_UA_POOL[0];
  let next = BROWSER_UA_POOL[Math.floor(Math.random() * BROWSER_UA_POOL.length)];
  // Avoid immediately reusing the same value when possible.
  for (let i = 0; i < 5 && next === current; i++) {
    next = BROWSER_UA_POOL[Math.floor(Math.random() * BROWSER_UA_POOL.length)];
  }
  return next;
}

const DefaultUserAgentPlaceholder = "Mozilla/5.0 … Chrome/146.0.0.0 Safari/537.36";

function countProxyLines(text: string): number {
  const seen = new Set<string>();
  for (const line of text.split(/\r?\n/)) {
    const value = line.trim();
    if (!value || value.startsWith("#")) continue;
    seen.add(value);
  }
  return seen.size;
}

function percent(value: number): string {
  if (!Number.isFinite(value)) return "—";
  return `${Math.round(value * 1000) / 10}%`;
}

function needsBrowserIdentity(scopes: EgressScope[]): boolean {
  return scopes.some((scope) => scope !== "grok_build");
}

function toggleScope(scopes: EgressScope[], scope: EgressScope, checked: boolean): EgressScope[] {
  if (checked) {
    if (scopes.includes(scope)) return scopes;
    return [...scopes, scope];
  }
  return scopes.filter((item) => item !== scope);
}

export function ProxiesPage() {
  const { t, i18n } = useTranslation();
  const queryClient = useQueryClient();
  const [editing, setEditing] = useState<EgressNodeDTO | null | undefined>(undefined);
  const [form, setForm] = useState<EgressNodeInput>(emptyInput);
  const [batchOpen, setBatchOpen] = useState(false);
  const [batchForm, setBatchForm] = useState<BatchImportForm>(emptyBatch);
  const [sort, setSort] = useState<TableSort>({ field: "", order: "asc" });
  const [testingId, setTestingId] = useState<string | null>(null);
  const [selected, setSelected] = useState<Set<string>>(new Set());

  const reportQuery = useQuery({ queryKey: ["egress-report"], queryFn: () => getEgressReport(), refetchInterval: 15_000 });
  const listQuery = useQuery({
    queryKey: ["egress-nodes", sort.field, sort.order],
    queryFn: () => listEgressNodes({ sortBy: sort.field || undefined, sortOrder: sort.field ? sort.order : undefined }),
  });

  const batchCount = useMemo(() => countProxyLines(batchForm.proxyText), [batchForm.proxyText]);
  const batchPreview = useMemo(() => {
    const prefix = batchForm.namePrefix.trim() || t("proxies.defaultNamePrefix");
    if (batchCount <= 0) return "";
    if (batchCount === 1) return `${prefix}#1`;
    if (batchCount === 2) return `${prefix}#1, ${prefix}#2`;
    return `${prefix}#1 … ${prefix}#${batchCount}`;
  }, [batchCount, batchForm.namePrefix, t]);

  const nodes = listQuery.data?.items ?? [];
  const selectedIds = useMemo(() => [...selected], [selected]);
  const allPageSelected = nodes.length > 0 && nodes.every((node) => selected.has(node.id));
  const somePageSelected = nodes.some((node) => selected.has(node.id));

  function invalidateAll() {
    void queryClient.invalidateQueries({ queryKey: ["egress-nodes"] });
    void queryClient.invalidateQueries({ queryKey: ["egress-report"] });
  }

  const save = useMutation({
    mutationFn: () => {
      const scopes = form.scopes.length > 0 ? form.scopes : [form.scope];
      const input: EgressNodeInput = {
        ...form,
        scopes,
        scope: scopes[0],
        proxyURL: form.proxyURL?.trim() || undefined,
        userAgent: needsBrowserIdentity(scopes) ? form.userAgent.trim() : "",
        cloudflareCookies: needsBrowserIdentity(scopes) ? form.cloudflareCookies?.trim() || undefined : undefined,
      };
      return editing ? updateEgressNode(editing.id, input) : createEgressNode(input);
    },
    onSuccess: () => { invalidateAll(); setEditing(undefined); toast.success(t("proxies.saved")); },
    onError: (error) => showError(error, t("proxies.operationFailed")),
  });
  const batchImport = useMutation({
    mutationFn: () => {
      const scopes = batchForm.scopes.length > 0 ? batchForm.scopes : (["grok_build"] as EgressScope[]);
      return createEgressNodesBatch({
        namePrefix: batchForm.namePrefix.trim() || t("proxies.defaultNamePrefix"),
        scope: scopes[0],
        scopes,
        enabled: batchForm.enabled,
        proxyText: batchForm.proxyText,
        userAgent: needsBrowserIdentity(scopes) ? batchForm.userAgent.trim() : "",
        cloudflareCookies: needsBrowserIdentity(scopes) ? batchForm.cloudflareCookies.trim() || undefined : undefined,
      });
    },
    onSuccess: (result) => {
      invalidateAll();
      setBatchOpen(false);
      setBatchForm(emptyBatch);
      toast.success(t("proxies.batchImported", { created: result.created, failed: result.failed }));
      if (result.errors.length > 0) toast.error(result.errors.slice(0, 3).join("；"));
    },
    onError: (error) => showError(error, t("proxies.operationFailed")),
  });
  const remove = useMutation({
    mutationFn: deleteEgressNode,
    onSuccess: () => { invalidateAll(); setSelected(new Set()); toast.success(t("proxies.deleted")); },
    onError: (error) => showError(error, t("proxies.operationFailed")),
  });
  const batchEnable = useMutation({
    mutationFn: (enabled: boolean) => setEgressNodesEnabled(selectedIds, enabled),
    onSuccess: (result) => {
      invalidateAll();
      setSelected(new Set());
      toast.success(t(result.enabled ? "proxies.batchEnabled" : "proxies.batchDisabled", { count: result.updated }));
    },
    onError: (error) => showError(error, t("proxies.operationFailed")),
  });
  const batchClearErrors = useMutation({
    mutationFn: () => clearEgressNodesErrors(selectedIds),
    onSuccess: (result) => {
      invalidateAll();
      toast.success(t("proxies.batchCleared", { count: result.cleared }));
    },
    onError: (error) => showError(error, t("proxies.operationFailed")),
  });
  const testOne = useMutation({
    mutationFn: (id: string) => testEgressNode(id),
    onMutate: (id) => setTestingId(id),
    onSettled: () => setTestingId(null),
    onSuccess: (result) => {
      invalidateAll();
      if (result.ok) toast.success(t("proxies.testPassed", { name: result.name, ms: result.latencyMs }));
      else toast.error(t("proxies.testFailed", { name: result.name, error: localizeProbeError(result.error || "failed", t) }));
    },
    onError: (error) => showError(error, t("proxies.operationFailed")),
  });
  const testAll = useMutation({
    mutationFn: () => testAllEgressNodes(),
    onSuccess: (result) => {
      invalidateAll();
      toast.success(t("proxies.testAllDone", { passed: result.passed, failed: result.failed, total: result.total }));
    },
    onError: (error) => showError(error, t("proxies.operationFailed")),
  });

  function openCreate() {
    setForm(emptyInput);
    setEditing(null);
  }

  function openBatch() {
    setBatchForm(emptyBatch);
    setBatchOpen(true);
  }

  function openEdit(node: EgressNodeDTO) {
    const scopes = node.scopes?.length ? node.scopes : [node.scope];
    const ua = node.userAgent?.trim() ?? "";
    // Legacy "random" sentinel → show empty; user can click 随机 to fill a concrete UA.
    const fixedUA = ua.toLowerCase() === "random" || ua.toLowerCase() === "auto" ? "" : ua;
    setForm({
      name: node.name,
      scope: scopes[0],
      scopes,
      enabled: node.enabled,
      userAgent: needsBrowserIdentity(scopes) ? fixedUA : "",
      proxyURL: "",
      cloudflareCookies: "",
    });
    setEditing(node);
  }

  function changeFormScopes(scopes: EgressScope[]) {
    const next = scopes.length > 0 ? scopes : (["grok_build"] as EgressScope[]);
    const previousDefault = listQuery.data?.defaultUserAgents[form.scopes[0] ?? form.scope] ?? "";
    const nextDefault = listQuery.data?.defaultUserAgents[next.find((s) => s !== "grok_build") ?? next[0]] ?? "";
    setForm({
      ...form,
      scopes: next,
      scope: next[0],
      userAgent: needsBrowserIdentity(next)
        ? (form.userAgent === "" || form.userAgent === previousDefault ? nextDefault : form.userAgent)
        : "",
      cloudflareCookies: needsBrowserIdentity(next) ? form.cloudflareCookies : "",
    });
  }

  function changeBatchScopes(scopes: EgressScope[]) {
    const next = scopes.length > 0 ? scopes : (["grok_build"] as EgressScope[]);
    const previousDefault = listQuery.data?.defaultUserAgents[batchForm.scopes[0]] ?? "";
    const nextDefault = listQuery.data?.defaultUserAgents[next.find((s) => s !== "grok_build") ?? next[0]] ?? "";
    setBatchForm({
      ...batchForm,
      scopes: next,
      userAgent: needsBrowserIdentity(next)
        ? (batchForm.userAgent === "" || batchForm.userAgent === previousDefault ? nextDefault : batchForm.userAgent)
        : "",
      cloudflareCookies: needsBrowserIdentity(next) ? batchForm.cloudflareCookies : "",
    });
  }

  function scopeLabel(scope: EgressScope) {
    if (scope === "grok_build") return t("settings.egress.scopeBuild");
    if (scope === "grok_console") return t("console.name");
    if (scope === "grok_web_asset") return t("settings.egress.scopeWebAsset");
    return t("settings.egress.scopeWeb");
  }

  function changeSort(field: string, initialOrder: SortOrder): void {
    setSort((current) => nextTableSort(current, field, initialOrder));
  }

  function toggleRow(id: string, checked: boolean) {
    setSelected((current) => {
      const next = new Set(current);
      if (checked) next.add(id);
      else next.delete(id);
      return next;
    });
  }

  function togglePage(checked: boolean) {
    setSelected((current) => {
      const next = new Set(current);
      for (const node of nodes) {
        if (checked) next.add(node.id);
        else next.delete(node.id);
      }
      return next;
    });
  }

  if (listQuery.isError) {
    return <ErrorState message={listQuery.error.message} onRetry={() => void listQuery.refetch()} />;
  }

  const report = reportQuery.data;
  const loading = listQuery.isPending;
  const formNeedsBrowser = needsBrowserIdentity(form.scopes);
  const batchNeedsBrowser = needsBrowserIdentity(batchForm.scopes);

  return (
    <div className="min-w-0 space-y-6">
      <header className="flex flex-col gap-4">
        <div className="min-w-0">
          <h1 className="text-xl font-medium">{t("proxies.title")}</h1>
          <p className="mt-1 text-xs text-muted-foreground">{t("proxies.description")}</p>
        </div>
        <div className="grid w-full grid-cols-2 gap-2 sm:flex sm:w-auto sm:flex-wrap sm:items-center sm:justify-end">
          <Button type="button" size="sm" variant="outline" className="w-full justify-center sm:w-auto" disabled={testAll.isPending || nodes.length === 0} onClick={() => testAll.mutate()}>
            {testAll.isPending ? <Spinner className="size-3.5" /> : <Zap className="size-3.5" />}
            {t("proxies.testAll")}
          </Button>
          <Button type="button" size="sm" variant="outline" className="w-full justify-center sm:w-auto" onClick={() => { void listQuery.refetch(); void reportQuery.refetch(); }}>
            <RefreshCw className={cn("size-3.5", (listQuery.isFetching || reportQuery.isFetching) && "animate-spin")} />
            {t("common.refresh")}
          </Button>
          <Button type="button" size="sm" variant="outline" className="w-full justify-center sm:w-auto" onClick={openBatch}>
            <FileUp className="size-3.5" />
            {t("proxies.batchImport")}
          </Button>
          <Button type="button" size="sm" variant="secondary" className="w-full justify-center sm:w-auto" onClick={openCreate}>
            <Plus className="size-3.5" />
            {t("proxies.add")}
          </Button>
        </div>
      </header>

      <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
        <StatCard icon={<Activity className="size-4" />} label={t("proxies.report.nodes")} value={`${report?.enabledNodes ?? 0} / ${report?.totalNodes ?? 0}`} hint={t("proxies.report.nodesHint", { proxy: report?.proxyNodes ?? 0, healthy: report?.healthyNodes ?? 0 })} />
        <StatCard icon={<CheckCircle2 className="size-4 text-emerald-500" />} label={t("proxies.report.successRate")} value={report && report.requestCount > 0 ? percent(report.successRate) : "—"} hint={t("proxies.report.successHint", { count: report?.successCount ?? 0, total: report?.requestCount ?? 0 })} />
        <StatCard icon={<XCircle className="size-4 text-destructive" />} label={t("proxies.report.failureRate")} value={report && report.requestCount > 0 ? percent(report.failureRate) : "—"} hint={t("proxies.report.failureHint", { count: report?.failureCount ?? 0, total: report?.requestCount ?? 0 })} />
        <StatCard icon={<Zap className="size-4" />} label={t("proxies.report.requests")} value={String(report?.requestCount ?? 0)} hint={t("proxies.report.requestsHint")} />
      </div>

      {selected.size > 0 ? (
        <div className="grid grid-cols-2 gap-2 rounded-md border bg-card p-2 sm:flex sm:flex-wrap sm:items-center">
          <span className="col-span-2 text-xs text-muted-foreground sm:col-span-1 sm:mr-1">{t("common.selectedCount", { count: selected.size })}</span>
          <Button size="sm" variant="secondary" className="w-full justify-center sm:w-auto" disabled={batchEnable.isPending} onClick={() => batchEnable.mutate(true)}>
            <Power className="size-3.5" />{t("common.enable")}
          </Button>
          <Button size="sm" variant="secondary" className="w-full justify-center sm:w-auto" disabled={batchEnable.isPending} onClick={() => batchEnable.mutate(false)}>
            {t("common.disable")}
          </Button>
          <Button size="sm" variant="outline" className="w-full justify-center sm:w-auto" disabled={batchClearErrors.isPending} onClick={() => batchClearErrors.mutate()}>
            <Eraser className="size-3.5" />{t("proxies.clearErrors")}
          </Button>
        </div>
      ) : null}

      <div className="min-w-0 overflow-x-auto rounded-md border">
        <Table className="min-w-[860px] table-fixed border-collapse text-xs">
          <colgroup>
            <col className="w-10" />
            <col className="w-[24%]" />
            <col className="w-[18%]" />
            <col className="w-[12%]" />
            <col className="w-[10%]" />
            <col className="w-[10%]" />
            <col className="w-[10%]" />
            <col className="w-[10%]" />
            <col className="w-12" />
          </colgroup>
          <TableHeader>
            <TableRow className="hover:bg-transparent">
              <TableCell className="h-9 px-2">
                <Checkbox
                  checked={allPageSelected ? true : somePageSelected ? "indeterminate" : false}
                  onCheckedChange={(checked) => togglePage(checked === true)}
                  aria-label={t("common.selectPage")}
                />
              </TableCell>
              <SortableTableHead field="name" sortBy={sort.field} sortOrder={sort.order} onSort={changeSort}>{t("proxies.name")}</SortableTableHead>
              <SortableTableHead field="scope" sortBy={sort.field} sortOrder={sort.order} onSort={changeSort}>{t("proxies.scope")}</SortableTableHead>
              <SortableTableHead field="proxy" sortBy={sort.field} sortOrder={sort.order} align="center" onSort={changeSort}>{t("proxies.proxy")}</SortableTableHead>
              <SortableTableHead field="health" sortBy={sort.field} sortOrder={sort.order} initialOrder="desc" align="center" onSort={changeSort}>{t("proxies.health")}</SortableTableHead>
              <SortableTableHead field="successRate" sortBy={sort.field} sortOrder={sort.order} initialOrder="desc" align="center" onSort={changeSort}>{t("proxies.successRate")}</SortableTableHead>
              <SortableTableHead field="failureRate" sortBy={sort.field} sortOrder={sort.order} initialOrder="desc" align="center" onSort={changeSort}>{t("proxies.failureRate")}</SortableTableHead>
              <SortableTableHead field="requests" sortBy={sort.field} sortOrder={sort.order} initialOrder="desc" align="center" onSort={changeSort}>{t("proxies.requests")}</SortableTableHead>
              <TableActionHead />
            </TableRow>
          </TableHeader>
          <TableBody>
            {loading ? (
              <TableRow><TableCell colSpan={9} className="h-24 text-center"><Spinner className="mx-auto size-4" /></TableCell></TableRow>
            ) : nodes.length === 0 ? (
              <TableRow><TableCell colSpan={9} className="h-24 text-center text-xs text-muted-foreground">{t("proxies.empty")}</TableCell></TableRow>
            ) : (
              nodes.map((node) => {
                const scopes = node.scopes?.length ? node.scopes : [node.scope];
                return (
                  <TableRow className="group" key={node.id} data-state={selected.has(node.id) ? "selected" : undefined}>
                    <TableCell>
                      <Checkbox checked={selected.has(node.id)} onCheckedChange={(checked) => toggleRow(node.id, checked === true)} aria-label={t("common.selectItem", { name: node.name })} />
                    </TableCell>
                    <TableCell className="min-w-0">
                      <div className="flex min-w-0 flex-wrap items-center gap-1.5">
                        <span className="truncate text-xs font-medium" title={node.name}>{node.name}</span>
                        {!node.enabled ? <Badge variant="outline" className="shrink-0">{t("common.disabled")}</Badge> : null}
                      </div>
                      {node.lastError ? (
                        <div className="mt-1 max-w-full">
                          <Badge variant="outline" className="max-w-full truncate border-destructive/40 font-normal text-[11px] text-destructive" title={node.lastError}>
                            {node.lastError}
                          </Badge>
                        </div>
                      ) : null}
                      {node.lastProbeAt ? (
                        <div className="mt-1 truncate text-[11px] text-muted-foreground" title={node.lastProbeOK
                          ? t("proxies.lastProbeOk", { ms: node.lastProbeMs ?? 0, time: formatDateTime(node.lastProbeAt, i18n.language) })
                          : t("proxies.lastProbeFail", { error: node.lastProbeError || "failed", time: formatDateTime(node.lastProbeAt, i18n.language) })}>
                          {node.lastProbeOK
                            ? t("proxies.lastProbeOk", { ms: node.lastProbeMs ?? 0, time: formatDateTime(node.lastProbeAt, i18n.language) })
                            : t("proxies.lastProbeFail", { error: localizeProbeError(node.lastProbeError || "failed", t), time: formatDateTime(node.lastProbeAt, i18n.language) })}
                        </div>
                      ) : null}
                    </TableCell>
                    <TableCell className="min-w-0">
                      <div className="flex max-w-full flex-wrap gap-1">
                        {scopes.map((scope) => (
                          <Badge key={scope} variant="outline" className="max-w-full shrink-0 font-normal">
                            {scopeLabel(scope)}
                          </Badge>
                        ))}
                      </div>
                    </TableCell>
                    <TableCell className="text-center">
                      {node.proxyConfigured ? (
                        <div className="flex flex-wrap items-center justify-center gap-1">
                          <Badge variant="secondary" className="font-mono text-[11px] uppercase tracking-wide">
                            {node.proxyProtocol || t("proxies.protocolUnknown")}
                          </Badge>
                        </div>
                      ) : (
                        <Badge variant="outline" className="font-normal text-muted-foreground">{t("proxies.direct")}</Badge>
                      )}
                    </TableCell>
                    <TableCell className="text-center text-xs tabular-nums">{Math.round(node.health * 100)}%</TableCell>
                    <TableCell className="text-center text-xs tabular-nums text-emerald-600 dark:text-emerald-400">{node.requestCount > 0 ? percent(node.successRate) : "—"}</TableCell>
                    <TableCell className="text-center text-xs tabular-nums text-destructive">{node.requestCount > 0 ? percent(node.failureRate) : "—"}</TableCell>
                    <TableCell className="text-center text-xs tabular-nums">
                      {node.requestCount}
                      {node.inflight > 0 ? <span className="ml-1 text-muted-foreground">· {node.inflight}</span> : null}
                    </TableCell>
                    <TableActionCell>
                      <DropdownMenu modal={false}>
                        <DropdownMenuTrigger asChild>
                          <Button type="button" variant="ghost" size="icon" className="size-8 shrink-0 touch-manipulation" aria-label={t("common.actions")}>
                            <MoreHorizontal />
                          </Button>
                        </DropdownMenuTrigger>
                        <DropdownMenuContent align="end" side="bottom" sideOffset={6} collisionPadding={12} className="z-[80] min-w-36">
                          <DropdownMenuItem disabled={testingId === node.id || testOne.isPending} onClick={() => testOne.mutate(node.id)}>
                            {testingId === node.id ? <Spinner className="size-3.5" /> : <Zap />}
                            {t("proxies.test")}
                          </DropdownMenuItem>
                          <DropdownMenuItem onClick={() => openEdit(node)}><Pencil />{t("common.edit")}</DropdownMenuItem>
                          <DropdownMenuSeparator />
                          <DropdownMenuItem className="text-destructive focus:text-destructive" onClick={() => remove.mutate(node.id)}>
                            <Trash2 />{t("common.delete")}
                          </DropdownMenuItem>
                        </DropdownMenuContent>
                      </DropdownMenu>
                    </TableActionCell>
                  </TableRow>
                );
              })
            )}
          </TableBody>
        </Table>
      </div>

      <Dialog open={batchOpen} onOpenChange={(open) => { if (!open) { setBatchOpen(false); setBatchForm(emptyBatch); } }}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t("proxies.batchImportTitle")}</DialogTitle>
            <DialogDescription>{t("proxies.batchImportDescription")}</DialogDescription>
          </DialogHeader>
          <div className="grid gap-4">
            <Field label={t("proxies.namePrefix")}>
              <Input className="border-transparent" value={batchForm.namePrefix} placeholder={t("proxies.defaultNamePrefix")} onChange={(event) => setBatchForm({ ...batchForm, namePrefix: event.target.value })} />
              <p className="text-[11px] text-muted-foreground">
                {batchCount > 0 ? t("proxies.batchNamePreview", { preview: batchPreview, count: batchCount }) : t("proxies.batchNameHint")}
              </p>
            </Field>
            <Field label={t("proxies.scope")}>
              <ScopeChecklist scopes={batchForm.scopes} onChange={changeBatchScopes} labelFor={scopeLabel} />
            </Field>
            <Field label={t("proxies.enabled")}>
              <div className="flex h-9 items-center">
                <Switch checked={batchForm.enabled} onCheckedChange={(enabled) => setBatchForm({ ...batchForm, enabled })} />
              </div>
            </Field>
            <Field label={t("proxies.proxyList")}>
              <Textarea
                className="min-h-36 border-transparent font-mono text-xs"
                placeholder={"socks5h://user:pass@host:1080\nvmess://...\nhttps://proxy.example:8443"}
                value={batchForm.proxyText}
                onChange={(event) => setBatchForm({ ...batchForm, proxyText: event.target.value })}
              />
              <p className="text-[11px] text-muted-foreground">{t("proxies.batchProxyHint")}</p>
            </Field>
            {batchNeedsBrowser ? (
              <Field label={t("proxies.userAgent")}>
                <div className="flex gap-2">
                  <Input
                    className="min-w-0 flex-1 border-transparent"
                    value={batchForm.userAgent}
                    placeholder={listQuery.data?.defaultUserAgents.grok_web || DefaultUserAgentPlaceholder}
                    onChange={(event) => setBatchForm({ ...batchForm, userAgent: event.target.value })}
                  />
                  <Button
                    type="button"
                    size="sm"
                    variant="outline"
                    className="shrink-0"
                    onClick={() => setBatchForm({ ...batchForm, userAgent: pickRandomUserAgent(batchForm.userAgent) })}
                  >
                    <Dices className="size-3.5" />
                    {t("proxies.randomUserAgent")}
                  </Button>
                </div>
                <p className="text-[11px] text-muted-foreground">{t("proxies.randomUserAgentHint")}</p>
              </Field>
            ) : null}
            {batchNeedsBrowser ? (
              <Field label={t("proxies.cloudflareCookie")}>
                <Input className="border-transparent" type="password" autoComplete="new-password" placeholder="cf_clearance=...; __cf_bm=..." value={batchForm.cloudflareCookies} onChange={(event) => setBatchForm({ ...batchForm, cloudflareCookies: event.target.value })} />
              </Field>
            ) : null}
          </div>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => { setBatchOpen(false); setBatchForm(emptyBatch); }}>{t("common.cancel")}</Button>
            <Button type="button" disabled={batchCount < 1 || batchImport.isPending} onClick={() => batchImport.mutate()}>
              {batchImport.isPending ? <Spinner className="size-3.5" /> : null}
              {t("proxies.batchImportAction", { count: batchCount })}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog open={editing !== undefined} onOpenChange={(open) => { if (!open) setEditing(undefined); }}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{editing ? t("proxies.editTitle") : t("proxies.addTitle")}</DialogTitle>
            <DialogDescription>{t("proxies.dialogDescription")}</DialogDescription>
          </DialogHeader>
          <div className="grid gap-4">
            <Field label={t("proxies.name")}>
              <Input className="border-transparent" value={form.name} onChange={(event) => setForm({ ...form, name: event.target.value })} />
            </Field>
            <Field label={t("proxies.scope")}>
              <ScopeChecklist scopes={form.scopes} onChange={changeFormScopes} labelFor={scopeLabel} />
              <p className="text-[11px] text-muted-foreground">{t("proxies.multiScopeHint")}</p>
            </Field>
            <Field label={t("proxies.enabled")}>
              <div className="flex h-9 items-center">
                <Switch checked={form.enabled} onCheckedChange={(enabled) => setForm({ ...form, enabled })} />
              </div>
            </Field>
            <Field label={t("proxies.proxyURL")}>
              <Input
                className="border-transparent"
                type="password"
                autoComplete="new-password"
                placeholder={editing?.proxyConfigured ? t("proxies.keepConfigured") : "socks5h://user:pass@host:port"}
                value={form.proxyURL}
                onChange={(event) => setForm({ ...form, proxyURL: event.target.value })}
              />
              <p className="text-[11px] text-muted-foreground">{t("proxies.proxyProtocols")}</p>
            </Field>
            {formNeedsBrowser ? (
              <Field label={t("proxies.userAgent")}>
                <div className="flex gap-2">
                  <Input
                    className="min-w-0 flex-1 border-transparent"
                    value={form.userAgent}
                    placeholder={listQuery.data?.defaultUserAgents.grok_web || DefaultUserAgentPlaceholder}
                    onChange={(event) => setForm({ ...form, userAgent: event.target.value })}
                  />
                  <Button
                    type="button"
                    size="sm"
                    variant="outline"
                    className="shrink-0"
                    onClick={() => setForm({ ...form, userAgent: pickRandomUserAgent(form.userAgent) })}
                  >
                    <Dices className="size-3.5" />
                    {t("proxies.randomUserAgent")}
                  </Button>
                </div>
                <p className="text-[11px] text-muted-foreground">{t("proxies.randomUserAgentHint")}</p>
              </Field>
            ) : null}
            {formNeedsBrowser ? (
              <Field label={t("proxies.cloudflareCookie")}>
                <Input
                  className="border-transparent"
                  type="password"
                  autoComplete="new-password"
                  placeholder={editing?.cookieConfigured ? t("proxies.keepConfigured") : "cf_clearance=...; __cf_bm=..."}
                  value={form.cloudflareCookies}
                  onChange={(event) => setForm({ ...form, cloudflareCookies: event.target.value })}
                />
              </Field>
            ) : null}
          </div>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => setEditing(undefined)}>{t("common.cancel")}</Button>
            <Button type="button" disabled={!form.name.trim() || form.scopes.length === 0 || save.isPending} onClick={() => save.mutate()}>{t("common.save")}</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}

function ScopeChecklist({
  scopes,
  onChange,
  labelFor,
}: {
  scopes: EgressScope[];
  onChange: (scopes: EgressScope[]) => void;
  labelFor: (scope: EgressScope) => string;
}) {
  return (
    <div className="flex flex-wrap gap-2">
      {EGRESS_SCOPES.map((scope) => {
        const checked = scopes.includes(scope);
        return (
          <label
            key={scope}
            className={cn(
              "inline-flex cursor-pointer items-center gap-1.5 rounded-full border px-2.5 py-1 text-xs transition-colors",
              checked ? "border-foreground/40 bg-secondary/70" : "border-border text-muted-foreground hover:border-foreground/30",
            )}
          >
            <Checkbox
              checked={checked}
              onCheckedChange={(value) => onChange(toggleScope(scopes, scope, value === true))}
              aria-label={labelFor(scope)}
            />
            <span>{labelFor(scope)}</span>
          </label>
        );
      })}
    </div>
  );
}

function StatCard({ icon, label, value, hint }: { icon: ReactNode; label: string; value: string; hint: string }) {
  return (
    <div className="rounded-lg border bg-card px-4 py-3">
      <div className="flex items-center gap-2 text-xs text-muted-foreground">
        {icon}
        <span>{label}</span>
      </div>
      <div className="mt-2 text-2xl font-medium tabular-nums tracking-tight">{value}</div>
      <p className="mt-1 text-[11px] text-muted-foreground">{hint}</p>
    </div>
  );
}

function Field({ label, className, children }: { label: string; className?: string; children: ReactNode }) {
  return <div className={className}><Label className="mb-1.5 text-xs">{label}</Label>{children}</div>;
}

function localizeProbeError(error: string, t: (key: string) => string): string {
  const lower = error.toLowerCase();
  if (lower.includes("anti-bot") || lower.includes("反爬")) return t("proxies.errors.antiBot");
  if (lower.includes("transport") || lower.includes("传输")) return t("proxies.errors.transport");
  if (lower.includes("timeout") || lower.includes("deadline") || lower.includes("超时")) return t("proxies.errors.timeout");
  if (lower.includes("connection refused") || lower.includes("连接被拒绝")) return t("proxies.errors.refused");
  if (lower.includes("no such host") || lower.includes("域名")) return t("proxies.errors.dns");
  return error;
}

function showError(error: unknown, fallback: string) {
  toast.error(error instanceof Error ? error.message : fallback);
}
