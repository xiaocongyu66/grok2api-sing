import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Activity, CheckCircle2, FileUp, MoreHorizontal, Pencil, Plus, RefreshCw, Trash2, XCircle, Zap } from "lucide-react";
import { type ReactNode, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuSeparator, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Spinner } from "@/components/ui/spinner";
import { Switch } from "@/components/ui/switch";
import { Table, TableActionCell, TableActionHead, TableBody, TableCell, TableHeader, TableRow } from "@/components/ui/table";
import { Textarea } from "@/components/ui/textarea";
import {
  createEgressNode,
  createEgressNodesBatch,
  deleteEgressNode,
  getEgressReport,
  listEgressNodes,
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

const emptyInput: EgressNodeInput = { name: "", scope: "grok_build", enabled: true, proxyURL: "", userAgent: "", cloudflareCookies: "" };

type BatchImportForm = {
  namePrefix: string;
  scope: EgressScope;
  enabled: boolean;
  proxyText: string;
  userAgent: string;
  cloudflareCookies: string;
};

const emptyBatch: BatchImportForm = {
  namePrefix: "代理",
  scope: "grok_build",
  enabled: true,
  proxyText: "",
  userAgent: "",
  cloudflareCookies: "",
};

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

export function ProxiesPage() {
  const { t, i18n } = useTranslation();
  const queryClient = useQueryClient();
  const [editing, setEditing] = useState<EgressNodeDTO | null | undefined>(undefined);
  const [form, setForm] = useState<EgressNodeInput>(emptyInput);
  const [batchOpen, setBatchOpen] = useState(false);
  const [batchForm, setBatchForm] = useState<BatchImportForm>(emptyBatch);
  const [sort, setSort] = useState<TableSort>({ field: "", order: "asc" });
  const [testingId, setTestingId] = useState<string | null>(null);

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

  function invalidateAll() {
    void queryClient.invalidateQueries({ queryKey: ["egress-nodes"] });
    void queryClient.invalidateQueries({ queryKey: ["egress-report"] });
  }

  const save = useMutation({
    mutationFn: () => {
      const input = {
        ...form,
        proxyURL: form.proxyURL?.trim() || undefined,
        userAgent: form.scope === "grok_build" ? "" : form.userAgent,
        cloudflareCookies: form.scope === "grok_build" ? undefined : form.cloudflareCookies?.trim() || undefined,
      };
      return editing ? updateEgressNode(editing.id, input) : createEgressNode(input);
    },
    onSuccess: () => { invalidateAll(); setEditing(undefined); toast.success(t("proxies.saved")); },
    onError: (error) => showError(error, t("proxies.operationFailed")),
  });
  const batchImport = useMutation({
    mutationFn: () => createEgressNodesBatch({
      namePrefix: batchForm.namePrefix.trim() || t("proxies.defaultNamePrefix"),
      scope: batchForm.scope,
      enabled: batchForm.enabled,
      proxyText: batchForm.proxyText,
      userAgent: batchForm.scope === "grok_build" ? "" : batchForm.userAgent,
      cloudflareCookies: batchForm.scope === "grok_build" ? undefined : batchForm.cloudflareCookies.trim() || undefined,
    }),
    onSuccess: (result) => {
      invalidateAll();
      setBatchOpen(false);
      setBatchForm(emptyBatch);
      toast.success(t("proxies.batchImported", { created: result.created, failed: result.failed }));
      if (result.errors.length > 0) {
        toast.error(result.errors.slice(0, 3).join("；"));
      }
    },
    onError: (error) => showError(error, t("proxies.operationFailed")),
  });
  const remove = useMutation({
    mutationFn: deleteEgressNode,
    onSuccess: () => { invalidateAll(); toast.success(t("proxies.deleted")); },
    onError: (error) => showError(error, t("proxies.operationFailed")),
  });
  const testOne = useMutation({
    mutationFn: (id: string) => testEgressNode(id),
    onMutate: (id) => setTestingId(id),
    onSettled: () => setTestingId(null),
    onSuccess: (result) => {
      invalidateAll();
      if (result.ok) toast.success(t("proxies.testPassed", { name: result.name, ms: result.latencyMs }));
      else toast.error(t("proxies.testFailed", { name: result.name, error: result.error || "failed" }));
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
    setBatchForm({
      ...emptyBatch,
      userAgent: listQuery.data?.defaultUserAgents.grok_build ?? "",
    });
    setBatchOpen(true);
  }

  function openEdit(node: EgressNodeDTO) {
    setForm({
      name: node.name,
      scope: node.scope,
      enabled: node.enabled,
      userAgent: node.scope === "grok_build" ? "" : node.userAgent,
      proxyURL: "",
      cloudflareCookies: "",
    });
    setEditing(node);
  }

  function changeScope(scope: EgressScope) {
    const previousDefault = listQuery.data?.defaultUserAgents[form.scope] ?? "";
    const nextDefault = listQuery.data?.defaultUserAgents[scope] ?? "";
    setForm({
      ...form,
      scope,
      userAgent: scope === "grok_build" ? "" : (form.userAgent === "" || form.userAgent === previousDefault ? nextDefault : form.userAgent),
      cloudflareCookies: scope === "grok_build" ? "" : form.cloudflareCookies,
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

  if (listQuery.isError) {
    return <ErrorState message={listQuery.error.message} onRetry={() => void listQuery.refetch()} />;
  }

  const report = reportQuery.data;
  const nodes = listQuery.data?.items ?? [];
  const loading = listQuery.isPending;

  return (
    <div className="space-y-6">
      <header className="flex flex-col gap-5 sm:flex-row sm:items-center sm:justify-between">
        <div className="min-w-0">
          <h1 className="text-xl font-medium">{t("proxies.title")}</h1>
          <p className="mt-1 text-xs text-muted-foreground">{t("proxies.description")}</p>
        </div>
        <div className="flex shrink-0 flex-wrap items-center gap-2">
          <Button type="button" size="sm" variant="outline" disabled={testAll.isPending || nodes.length === 0} onClick={() => testAll.mutate()}>
            {testAll.isPending ? <Spinner className="size-3.5" /> : <Zap className="size-3.5" />}
            {t("proxies.testAll")}
          </Button>
          <Button type="button" size="sm" variant="outline" onClick={() => { void listQuery.refetch(); void reportQuery.refetch(); }}>
            <RefreshCw className={cn("size-3.5", (listQuery.isFetching || reportQuery.isFetching) && "animate-spin")} />
            {t("common.refresh")}
          </Button>
          <Button type="button" size="sm" variant="outline" onClick={openBatch}>
            <FileUp className="size-3.5" />
            {t("proxies.batchImport")}
          </Button>
          <Button type="button" size="sm" variant="secondary" onClick={openCreate}>
            <Plus className="size-3.5" />
            {t("proxies.add")}
          </Button>
        </div>
      </header>

      <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
        <StatCard
          icon={<Activity className="size-4" />}
          label={t("proxies.report.nodes")}
          value={`${report?.enabledNodes ?? 0} / ${report?.totalNodes ?? 0}`}
          hint={t("proxies.report.nodesHint", { proxy: report?.proxyNodes ?? 0, healthy: report?.healthyNodes ?? 0 })}
        />
        <StatCard
          icon={<CheckCircle2 className="size-4 text-emerald-500" />}
          label={t("proxies.report.successRate")}
          value={report && report.requestCount > 0 ? percent(report.successRate) : "—"}
          hint={t("proxies.report.successHint", { count: report?.successCount ?? 0, total: report?.requestCount ?? 0 })}
        />
        <StatCard
          icon={<XCircle className="size-4 text-destructive" />}
          label={t("proxies.report.failureRate")}
          value={report && report.requestCount > 0 ? percent(report.failureRate) : "—"}
          hint={t("proxies.report.failureHint", { count: report?.failureCount ?? 0, total: report?.requestCount ?? 0 })}
        />
        <StatCard
          icon={<Zap className="size-4" />}
          label={t("proxies.report.requests")}
          value={String(report?.requestCount ?? 0)}
          hint={t("proxies.report.requestsHint")}
        />
      </div>

      <div className="min-w-0 overflow-x-auto rounded-md border">
        <Table className="min-w-[920px] table-fixed border-collapse text-xs">
          <colgroup>
            <col className="w-[22%]" />
            <col className="w-[12%]" />
            <col className="w-[14%]" />
            <col className="w-[10%]" />
            <col className="w-[10%]" />
            <col className="w-[10%]" />
            <col className="w-[12%]" />
            <col className="w-12" />
          </colgroup>
          <TableHeader>
            <TableRow className="hover:bg-transparent">
              <SortableTableHead field="name" sortBy={sort.field} sortOrder={sort.order} onSort={changeSort}>{t("proxies.name")}</SortableTableHead>
              <SortableTableHead field="scope" sortBy={sort.field} sortOrder={sort.order} align="center" onSort={changeSort}>{t("proxies.scope")}</SortableTableHead>
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
              <TableRow>
                <TableCell colSpan={8} className="h-24 text-center">
                  <Spinner className="mx-auto size-4" />
                </TableCell>
              </TableRow>
            ) : nodes.length === 0 ? (
              <TableRow>
                <TableCell colSpan={8} className="h-24 text-center text-xs text-muted-foreground">
                  {t("proxies.empty")}
                </TableCell>
              </TableRow>
            ) : (
              nodes.map((node) => (
                <TableRow className="group" key={node.id}>
                  <TableCell className="min-w-0">
                    <div className="flex min-w-0 items-center gap-2">
                      <div className="truncate text-xs font-medium">{node.name}</div>
                      {!node.enabled ? <Badge variant="outline" className="shrink-0">{t("common.disabled")}</Badge> : null}
                    </div>
                    {node.lastError ? <div className="mt-0.5 truncate text-[11px] text-destructive" title={node.lastError}>{node.lastError}</div> : null}
                    {node.lastProbeAt ? (
                      <div className="mt-0.5 truncate text-[11px] text-muted-foreground">
                        {node.lastProbeOK
                          ? t("proxies.lastProbeOk", { ms: node.lastProbeMs ?? 0, time: formatDateTime(node.lastProbeAt, i18n.language) })
                          : t("proxies.lastProbeFail", { error: node.lastProbeError || "failed", time: formatDateTime(node.lastProbeAt, i18n.language) })}
                      </div>
                    ) : null}
                  </TableCell>
                  <TableCell className="text-center"><Badge variant="outline" className="max-w-full truncate">{scopeLabel(node.scope)}</Badge></TableCell>
                  <TableCell className="text-center">
                    {node.proxyConfigured ? (
                      <div className="flex flex-col items-center gap-0.5">
                        <Badge variant="secondary" className="font-mono text-[11px] uppercase tracking-wide">
                          {node.proxyProtocol || t("proxies.protocolUnknown")}
                        </Badge>
                        <span className="text-[11px] text-muted-foreground">{t("proxies.configured")}</span>
                      </div>
                    ) : (
                      <span className="text-xs text-muted-foreground">{t("proxies.direct")}</span>
                    )}
                  </TableCell>
                  <TableCell className="text-center text-xs tabular-nums">{Math.round(node.health * 100)}%</TableCell>
                  <TableCell className="text-center text-xs tabular-nums text-emerald-600 dark:text-emerald-400">
                    {node.requestCount > 0 ? percent(node.successRate) : "—"}
                  </TableCell>
                  <TableCell className="text-center text-xs tabular-nums text-destructive">
                    {node.requestCount > 0 ? percent(node.failureRate) : "—"}
                  </TableCell>
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
              ))
            )}
          </TableBody>
        </Table>
      </div>

      <Dialog open={batchOpen} onOpenChange={(open) => { if (!open) { setBatchOpen(false); setBatchForm(emptyBatch); } }}>
        <DialogContent className="max-h-[90vh] overflow-y-auto">
          <DialogHeader>
            <DialogTitle>{t("proxies.batchImportTitle")}</DialogTitle>
            <DialogDescription>{t("proxies.batchImportDescription")}</DialogDescription>
          </DialogHeader>
          <div className="grid gap-4 sm:grid-cols-2">
            <Field label={t("proxies.namePrefix")} className="sm:col-span-2">
              <Input
                className="border-transparent"
                value={batchForm.namePrefix}
                placeholder={t("proxies.defaultNamePrefix")}
                onChange={(event) => setBatchForm({ ...batchForm, namePrefix: event.target.value })}
              />
              <p className="text-[11px] text-muted-foreground">
                {batchCount > 0
                  ? t("proxies.batchNamePreview", { preview: batchPreview, count: batchCount })
                  : t("proxies.batchNameHint")}
              </p>
            </Field>
            <Field label={t("proxies.scope")}>
              <Select
                value={batchForm.scope}
                onValueChange={(value) => {
                  const scope = value as EgressScope;
                  const previousDefault = listQuery.data?.defaultUserAgents[batchForm.scope] ?? "";
                  const nextDefault = listQuery.data?.defaultUserAgents[scope] ?? "";
                  setBatchForm({
                    ...batchForm,
                    scope,
                    userAgent: scope === "grok_build" ? "" : (batchForm.userAgent === "" || batchForm.userAgent === previousDefault ? nextDefault : batchForm.userAgent),
                    cloudflareCookies: scope === "grok_build" ? "" : batchForm.cloudflareCookies,
                  });
                }}
              >
                <SelectTrigger className="border-transparent"><SelectValue /></SelectTrigger>
                <SelectContent>
                  <SelectItem value="grok_build">{t("settings.egress.scopeBuild")}</SelectItem>
                  <SelectItem value="grok_web">{t("settings.egress.scopeWeb")}</SelectItem>
                  <SelectItem value="grok_console">{t("console.name")}</SelectItem>
                  <SelectItem value="grok_web_asset">{t("settings.egress.scopeWebAsset")}</SelectItem>
                </SelectContent>
              </Select>
            </Field>
            <Field label={t("proxies.enabled")}>
              <div className="flex h-9 items-center">
                <Switch checked={batchForm.enabled} onCheckedChange={(enabled) => setBatchForm({ ...batchForm, enabled })} />
              </div>
            </Field>
            <Field label={t("proxies.proxyList")} className="sm:col-span-2">
              <Textarea
                className="min-h-40 border-transparent font-mono text-xs"
                placeholder={"socks5h://user:pass@host:1080\nvmess://...\nhttps://proxy.example:8443"}
                value={batchForm.proxyText}
                onChange={(event) => setBatchForm({ ...batchForm, proxyText: event.target.value })}
              />
              <p className="text-[11px] text-muted-foreground">{t("proxies.batchProxyHint")}</p>
            </Field>
            {batchForm.scope !== "grok_build" ? (
              <Field label={t("proxies.userAgent")} className="sm:col-span-2">
                <Input className="border-transparent" value={batchForm.userAgent} onChange={(event) => setBatchForm({ ...batchForm, userAgent: event.target.value })} />
              </Field>
            ) : null}
            {batchForm.scope !== "grok_build" ? (
              <Field label={t("proxies.cloudflareCookie")} className="sm:col-span-2">
                <Input
                  className="border-transparent"
                  type="password"
                  autoComplete="new-password"
                  placeholder="cf_clearance=...; __cf_bm=..."
                  value={batchForm.cloudflareCookies}
                  onChange={(event) => setBatchForm({ ...batchForm, cloudflareCookies: event.target.value })}
                />
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
        <DialogContent className="max-h-[90vh] overflow-y-auto">
          <DialogHeader>
            <DialogTitle>{editing ? t("proxies.editTitle") : t("proxies.addTitle")}</DialogTitle>
            <DialogDescription>{t("proxies.dialogDescription")}</DialogDescription>
          </DialogHeader>
          <div className="grid gap-4 sm:grid-cols-2">
            <Field label={t("proxies.name")} className="sm:col-span-2">
              <Input className="border-transparent" value={form.name} onChange={(event) => setForm({ ...form, name: event.target.value })} />
            </Field>
            <Field label={t("proxies.scope")}>
              <Select value={form.scope} onValueChange={(value) => changeScope(value as EgressScope)}>
                <SelectTrigger className="border-transparent"><SelectValue /></SelectTrigger>
                <SelectContent>
                  <SelectItem value="grok_build">{t("settings.egress.scopeBuild")}</SelectItem>
                  <SelectItem value="grok_web">{t("settings.egress.scopeWeb")}</SelectItem>
                  <SelectItem value="grok_console">{t("console.name")}</SelectItem>
                  <SelectItem value="grok_web_asset">{t("settings.egress.scopeWebAsset")}</SelectItem>
                </SelectContent>
              </Select>
            </Field>
            <Field label={t("proxies.enabled")}>
              <div className="flex h-9 items-center">
                <Switch checked={form.enabled} onCheckedChange={(enabled) => setForm({ ...form, enabled })} />
              </div>
            </Field>
            <Field label={t("proxies.proxyURL")} className="sm:col-span-2">
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
            {form.scope !== "grok_build" ? (
              <Field label={t("proxies.userAgent")} className="sm:col-span-2">
                <Input className="border-transparent" value={form.userAgent} onChange={(event) => setForm({ ...form, userAgent: event.target.value })} />
              </Field>
            ) : null}
            {form.scope !== "grok_build" ? (
              <Field label={t("proxies.cloudflareCookie")} className="sm:col-span-2">
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
            <Button type="button" disabled={!form.name.trim() || save.isPending} onClick={() => save.mutate()}>{t("common.save")}</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
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

function showError(error: unknown, fallback: string) {
  toast.error(error instanceof Error ? error.message : fallback);
}
