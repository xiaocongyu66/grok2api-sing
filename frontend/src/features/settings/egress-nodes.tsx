import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { MoreHorizontal, Pencil, Plus, Trash2 } from "lucide-react";
import { type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuSeparator, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { Table, TableActionCell, TableActionHead, TableBody, TableCell, TableHeader, TableRow } from "@/components/ui/table";
import { createEgressNode, deleteEgressNode, listEgressNodes, updateEgressNode, type EgressNodeDTO, type EgressNodeInput, type EgressScope } from "@/features/settings/settings-api";
import { SortableTableHead } from "@/shared/components/sortable-table-head";
import { nextTableSort, type SortOrder, type TableSort } from "@/shared/lib/table-sort";

const emptyInput: EgressNodeInput = { name: "", scope: "grok_build", enabled: true, proxyURL: "", userAgent: "", cloudflareCookies: "" };

export function EgressNodes() {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const [editing, setEditing] = useState<EgressNodeDTO | null | undefined>(undefined);
  const [form, setForm] = useState<EgressNodeInput>(emptyInput);
  const [sort, setSort] = useState<TableSort>({ field: "", order: "asc" });
  const query = useQuery({ queryKey: ["egress-nodes", sort.field, sort.order], queryFn: () => listEgressNodes({ sortBy: sort.field || undefined, sortOrder: sort.field ? sort.order : undefined }) });
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
    onSuccess: () => { void queryClient.invalidateQueries({ queryKey: ["egress-nodes"] }); setEditing(undefined); toast.success(t("settings.egress.saved")); },
    onError: (error) => showError(error, t("settings.egress.operationFailed")),
  });
  const remove = useMutation({
    mutationFn: deleteEgressNode,
    onSuccess: () => { void queryClient.invalidateQueries({ queryKey: ["egress-nodes"] }); toast.success(t("settings.egress.deleted")); },
    onError: (error) => showError(error, t("settings.egress.operationFailed")),
  });

  function openCreate() {
    setForm(emptyInput);
    setEditing(null);
  }

  function openEdit(node: EgressNodeDTO) {
    setForm({ name: node.name, scope: node.scope, enabled: node.enabled, userAgent: node.scope === "grok_build" ? "" : node.userAgent, proxyURL: "", cloudflareCookies: "" });
    setEditing(node);
  }

  function changeScope(scope: EgressScope) {
    const previousDefault = query.data?.defaultUserAgents[form.scope] ?? "";
    const nextDefault = query.data?.defaultUserAgents[scope] ?? "";
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

  const nodes = query.data?.items ?? [];
  return (
    <div className="space-y-3">
      <div className="flex items-center justify-between gap-3">
        <p className="text-xs text-muted-foreground">{t("console.egressDescription")}</p>
        <Button type="button" size="sm" variant="secondary" onClick={openCreate}><Plus />{t("settings.egress.add")}</Button>
      </div>
      <div className="min-w-0 overflow-x-auto rounded-md border">
        <Table className="min-w-[720px] table-fixed border-collapse text-xs">
          <TableHeader>
            <TableRow className="hover:bg-transparent">
              <SortableTableHead field="name" sortBy={sort.field} sortOrder={sort.order} onSort={changeSort}>{t("settings.egress.name")}</SortableTableHead>
              <SortableTableHead field="scope" sortBy={sort.field} sortOrder={sort.order} align="center" onSort={changeSort}>{t("settings.egress.scope")}</SortableTableHead>
              <SortableTableHead field="proxy" sortBy={sort.field} sortOrder={sort.order} align="center" onSort={changeSort}>{t("settings.egress.proxy")}</SortableTableHead>
              <SortableTableHead field="clearance" sortBy={sort.field} sortOrder={sort.order} align="center" onSort={changeSort}>{t("settings.egress.clearance")}</SortableTableHead>
              <SortableTableHead field="health" sortBy={sort.field} sortOrder={sort.order} initialOrder="desc" align="center" onSort={changeSort}>{t("settings.egress.health")}</SortableTableHead>
              <TableActionHead />
            </TableRow>
          </TableHeader>
          <TableBody>
            {nodes.length === 0 ? <TableRow><TableCell colSpan={6} className="h-20 text-center text-xs text-muted-foreground">{t("settings.egress.directFallback")}</TableCell></TableRow> : nodes.map((node) => (
              <TableRow className="group" key={node.id}>
                <TableCell className="min-w-0"><div className="truncate text-xs font-medium">{node.name}</div>{node.lastError ? <div className="mt-0.5 truncate text-[11px] text-destructive" title={node.lastError}>{node.lastError}</div> : null}</TableCell>
                <TableCell className="text-center"><Badge variant="outline" className="max-w-full truncate">{scopeLabel(node.scope)}</Badge></TableCell>
                <TableCell className="text-center">
                  {node.proxyConfigured ? (
                    <div className="flex flex-col items-center gap-0.5">
                      <Badge variant="secondary" className="font-mono text-[11px] uppercase tracking-wide">{node.proxyProtocol || t("proxies.protocolUnknown")}</Badge>
                      <span className="text-[11px] text-muted-foreground">{t("settings.egress.configured")}</span>
                    </div>
                  ) : (
                    <span className="text-xs text-muted-foreground">{t("settings.egress.direct")}</span>
                  )}
                </TableCell>
                <TableCell className="text-center text-xs text-muted-foreground">{node.cookieConfigured ? t("settings.egress.configured") : t("settings.egress.none")}</TableCell>
                <TableCell className="text-center text-xs tabular-nums">{Math.round(node.health * 100)}%</TableCell>
                <TableActionCell>
                  <DropdownMenu modal={false}>
                    <DropdownMenuTrigger asChild>
                      <Button type="button" variant="ghost" size="icon" className="size-8 shrink-0 touch-manipulation" aria-label={t("common.actions")}>
                        <MoreHorizontal />
                      </Button>
                    </DropdownMenuTrigger>
                    <DropdownMenuContent align="end" side="bottom" sideOffset={6} collisionPadding={12} className="z-[80] min-w-36">
                      <DropdownMenuItem onClick={() => openEdit(node)}><Pencil />{t("common.edit")}</DropdownMenuItem>
                      <DropdownMenuSeparator />
                      <DropdownMenuItem className="text-destructive focus:text-destructive" onClick={() => remove.mutate(node.id)}><Trash2 />{t("common.delete")}</DropdownMenuItem>
                    </DropdownMenuContent>
                  </DropdownMenu>
                </TableActionCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </div>

      <Dialog open={editing !== undefined} onOpenChange={(open) => { if (!open) setEditing(undefined); }}>
        <DialogContent className="max-h-[90vh] overflow-y-auto">
          <DialogHeader><DialogTitle>{editing ? t("settings.egress.editTitle") : t("settings.egress.addTitle")}</DialogTitle><DialogDescription>{t("console.egressDialogDescription")}</DialogDescription></DialogHeader>
          <div className="grid gap-4 sm:grid-cols-2">
            <Field label={t("settings.egress.name")} className="sm:col-span-2"><Input className="border-transparent" value={form.name} onChange={(event) => setForm({ ...form, name: event.target.value })} /></Field>
            <Field label={t("settings.egress.scope")}><Select value={form.scope} onValueChange={(value) => changeScope(value as EgressScope)}><SelectTrigger className="border-transparent"><SelectValue /></SelectTrigger><SelectContent><SelectItem value="grok_build">{t("settings.egress.scopeBuild")}</SelectItem><SelectItem value="grok_web">{t("settings.egress.scopeWeb")}</SelectItem><SelectItem value="grok_console">{t("console.name")}</SelectItem><SelectItem value="grok_web_asset">{t("settings.egress.scopeWebAsset")}</SelectItem></SelectContent></Select></Field>
            <Field label={t("settings.egress.enabled")}><div className="flex h-9 items-center"><Switch checked={form.enabled} onCheckedChange={(enabled) => setForm({ ...form, enabled })} /></div></Field>
            <Field label={t("settings.egress.proxyURL")} className="sm:col-span-2"><Input className="border-transparent" type="password" autoComplete="new-password" placeholder={editing?.proxyConfigured ? t("settings.egress.keepConfigured") : "socks5h://user:pass@host:port"} value={form.proxyURL} onChange={(event) => setForm({ ...form, proxyURL: event.target.value })} /><p className="text-[11px] text-muted-foreground">{t("settings.egress.proxyProtocols")}</p></Field>
            {form.scope !== "grok_build" ? <Field label={t("settings.egress.userAgent")} className="sm:col-span-2"><Input className="border-transparent" value={form.userAgent} onChange={(event) => setForm({ ...form, userAgent: event.target.value })} /></Field> : null}
            {form.scope !== "grok_build" ? <Field label={t("settings.egress.cloudflareCookie")} className="sm:col-span-2"><Input className="border-transparent" type="password" autoComplete="new-password" placeholder={editing?.cookieConfigured ? t("settings.egress.keepConfigured") : "cf_clearance=...; __cf_bm=..."} value={form.cloudflareCookies} onChange={(event) => setForm({ ...form, cloudflareCookies: event.target.value })} /></Field> : null}
          </div>
          <DialogFooter><Button type="button" variant="outline" onClick={() => setEditing(undefined)}>{t("common.cancel")}</Button><Button type="button" disabled={!form.name.trim() || save.isPending} onClick={() => save.mutate()}>{t("common.save")}</Button></DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}

function Field({ label, className, children }: { label: string; className?: string; children: ReactNode }) {
  return <div className={className}><Label className="mb-1.5 text-xs">{label}</Label>{children}</div>;
}

function showError(error: unknown, fallback: string) {
  toast.error(error instanceof Error ? error.message : fallback);
}
