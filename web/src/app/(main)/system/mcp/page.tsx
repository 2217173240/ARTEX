"use client";

import { useI18n } from "@/lib/i18n";
import { DeleteConfirmation } from "@/components/delete-confirmation";

import * as React from "react";
import { toast } from "sonner";
import { PlusIcon, RefreshCwIcon, ServerIcon, Trash2Icon } from "lucide-react";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import { Badge } from "@/components/ui/badge";
import { Switch } from "@/components/ui/switch";
import { Checkbox } from "@/components/ui/checkbox";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Sheet, SheetContent, SheetHeader, SheetTitle, SheetDescription } from "@/components/ui/sheet";
import { Tabs, TabsList, TabsTrigger, TabsContent } from "@/components/ui/tabs";
import { AlertDialog, AlertDialogContent, AlertDialogHeader, AlertDialogTitle, AlertDialogDescription, AlertDialogFooter, AlertDialogCancel, AlertDialogAction } from "@/components/ui/alert-dialog";
import { parseMcpArgs, mcpApprovalDestination } from "@/lib/mcp-approval";
import { api } from "@/lib/api";
import type { MCPServer, MCPTool, Agent } from "@/lib/types";

type Transport = "stdio" | "http" | "sse";
type FormState = {
  name: string;
  transport: Transport;
  command: string;
  args: string;
  url: string;
  env: string;
  insecure: boolean;
  clearEnv: boolean;
};
const emptyForm: FormState = {
  name: "",
  transport: "stdio",
  command: "",
  args: "[]",
  url: "",
  env: "",
  insecure: false,
  clearEnv: false,
};

export default function MCPPage() {
  const { t: uiText } = useI18n();
  const [servers, setServers] = React.useState<MCPServer[]>([]);
  const [agents, setAgents] = React.useState<Agent[]>([]);
  const [visibility, setVisibility] = React.useState<Record<number, string[]>>({});

  const [open, setOpen] = React.useState(false);
  const [editing, setEditing] = React.useState<MCPServer | null>(null); // null = add mode
  const [tab, setTab] = React.useState<"config" | "tools">("config");
  const [form, setForm] = React.useState<FormState>(emptyForm);
  const [saving, setSaving] = React.useState(false);
  const [tools, setTools] = React.useState<MCPTool[]>([]);
  const [toolsLoading, setToolsLoading] = React.useState(false);
  const [approval, setApproval] = React.useState<MCPServer | null>(null);
  const [mutating, setMutating] = React.useState(false);
  const mutationLock = React.useRef(false);
  const saveLock = React.useRef(false);
  const cancelApprovalRef = React.useRef<HTMLButtonElement>(null);
  const approvalLauncherRef = React.useRef<HTMLElement | null>(null);
  const savedForm = React.useRef(JSON.stringify(emptyForm));
  const dirty = JSON.stringify(form) !== savedForm.current;
  const [refreshing, setRefreshing] = React.useState(false);

  const load = React.useCallback(() => {
    api
      .agents()
      .then(setAgents)
      .catch(() => {});
    api
      .mcpServers()
      .then((ss) => {
        setServers(ss);
        ss.forEach((s) =>
          api
            .resourceVisibility("mcp", s.id)
            .then((ids) => setVisibility((v) => ({ ...v, [s.id]: ids })))
            .catch(() => {}),
        );
      })
      .catch(() => {});
  }, []);
  React.useEffect(() => {
    load();
  }, [load]);

  function setF(patch: Partial<FormState>) {
    setForm((f) => ({ ...f, ...patch }));
  }

  function parseEnv(text: string): Record<string, string> {
    const out: Record<string, string> = {};
    for (const line of text.split("\n")) {
      const trimmed = line.trim();
      if (!trimmed) continue;
      const idx = trimmed.indexOf("=");
      if (idx <= 0) throw new Error(uiText("每行凭据必须使用 KEY=VALUE 格式"));
      if (idx > 0) {
        const key = trimmed.slice(0, idx).trim();
        if (key) out[key] = trimmed.slice(idx + 1).trim();
      }
    }
    return out;
  }
  function openAdd() {
    setEditing(null);
    setForm(emptyForm);
    savedForm.current = JSON.stringify(emptyForm);
    setTools([]);
    setTab("config");
    setOpen(true);
  }

  function openEdit(s: MCPServer) {
    setEditing(s);
    const nextForm = {
      name: s.name,
      transport: s.transport,
      command: s.command ?? "",
      args: JSON.stringify(s.args ?? []),
      url: s.url ?? "",
      env: "",
      clearEnv: false,
      insecure: s.insecure ?? false,
    };
    setForm(nextForm);
    savedForm.current = JSON.stringify(nextForm);
    setTab("config");
    setOpen(true);
    loadTools(s.id);
  }

  async function loadTools(id: number) {
    setToolsLoading(true);
    try {
      setTools(await api.mcpTools(id));
    } catch {
      setTools([]);
    } finally {
      setToolsLoading(false);
    }
  }

  async function saveForm() {
    if (saveLock.current || mutationLock.current) return;
    if (!form.name.trim()) {
      toast.error(uiText("请填写名称"));
      return;
    }
    if (form.transport === "stdio" && !form.command.trim()) {
      toast.error(uiText("请填写命令"));
      return;
    }
    if (form.transport !== "stdio" && !form.url.trim()) {
      toast.error(uiText("请填写远程 URL"));
      return;
    }
    saveLock.current = true;
    setSaving(true);
    try {
      const base =
        form.transport !== "stdio"
          ? {
              transport: form.transport,
              url: form.url.trim(),
              command: "",
              args: [] as string[],
              ...(form.env.trim() || form.clearEnv || !editing ? { env: form.clearEnv ? {} : parseEnv(form.env) } : {}),
              insecure: form.insecure,
            }
          : {
              transport: "stdio" as const,
              command: form.command.trim(),
              args: parseMcpArgs(form.args),
              ...(form.env.trim() || form.clearEnv || !editing ? { env: form.clearEnv ? {} : parseEnv(form.env) } : {}),
              insecure: false,
            };
      const result = await api.saveMcpServer({
        ...(editing ? { id: editing.id, revision: editing.revision } : {}),
        name: form.name.trim(),
        ...base,
      });
      setEditing({ ...editing, ...base, ...result, name: form.name.trim(), env: undefined, env_keys: base.env ? Object.keys(base.env) : editing?.env_keys ?? [] });
      savedForm.current = JSON.stringify({ ...form, env: "", clearEnv: false });
      setForm((old) => ({ ...old, env: "", clearEnv: false }));
      loadTools(result.id);
      toast.success(uiText("配置已保存；连接权限变更需显式授权。"));
      load();
    } catch (e) {
      if ((e as { status?: number } | null)?.status === 409) {
        load();
        toast.error(uiText("配置版本已变化，请重新打开并核对配置；本次输入仍保留。"));
      } else toast.error(uiText("保存失败：") + (e as Error).message);
    } finally {
      saveLock.current = false;
      setSaving(false);
    }
  }

  async function refreshTools() {
    if (!editing || !editing.approved || !editing.enabled || dirty || refreshing) return;
    setRefreshing(true);
    try {
      const t = await api.refreshMcpServer(editing.id);
      setTools(t);
      toast.success(uiText("发现 {v0} 个工具", { v0: t.length }));
      load();
    } catch (e) {
      toast.error(uiText("刷新失败：") + (e as Error).message);
    } finally {
      setRefreshing(false);
    }
  }

  async function removeServer(s: MCPServer) {
    try {
      await api.deleteMcpServer(s.id);
      toast.success(uiText("已删除：{v0}", { v0: s.name }));
      setOpen(false);
      load();
      return true;
    } catch (e) {
      toast.error(uiText("删除失败：") + (e as Error).message);
      return false;
    }
  }

  async function requestApproval(s: MCPServer) {
    if (mutationLock.current || saveLock.current) return;
    const launcher = document.activeElement;
    approvalLauncherRef.current = launcher instanceof HTMLElement && launcher !== document.body ? launcher : null;
    mutationLock.current = true;
    setMutating(true);
    try {
      const current = (await api.mcpServers()).find((row) => row.id === s.id);
      if (!current) throw new Error(uiText("服务器不存在"));
      setApproval(current);
    } catch (e) {
      toast.error(uiText("操作失败：") + (e as Error).message);
    } finally { mutationLock.current = false; setMutating(false); }
  }

  async function mutateServer(s: MCPServer, approve: boolean) {
    if (mutationLock.current || saveLock.current) return;
    mutationLock.current = true;
    setMutating(true);
    try {
      const result = approve ? await api.approveMcpServer(s.id, s.revision) : await api.disableMcpServer(s.id, s.revision);
      if (editing?.id === s.id) setEditing((old) => old ? { ...old, ...result } : old);
      setApproval(null);
      load();
      toast.success(approve ? uiText("已授权并启用当前配置") : uiText("已停用并撤销授权"));
    } catch (e) {
      if ((e as { status?: number } | null)?.status === 409) {
        setApproval(null);
        load();
        toast.error(uiText("配置版本已变化，请重新打开并核对配置；本次输入仍保留。"));
      } else toast.error(uiText("操作失败：") + (e as Error).message);
    } finally { mutationLock.current = false; setMutating(false); }
  }

  async function toggleVisibility(serverId: number, agentId: string, agentName: string) {
    const on = (visibility[serverId] ?? []).includes(agentId);
    try {
      await api.toggleVisibility(agentId, "mcp", serverId, !on);
      toast.success(uiText("{v0}「{v1}」可见", { v0: on ? "取消" : "授予", v1: agentName }));
      load();
    } catch (e) {
      toast.error(uiText("操作失败：") + (e as Error).message);
    }
  }

  function renderForm() {
    return (
      <fieldset disabled={saving || mutating} className="grid gap-4 py-4">
        <div className="grid gap-2">
          <Label>{uiText("传输方式")}</Label>
          <div className="flex gap-2">
            <Button
              type="button"
              variant={form.transport === "stdio" ? "default" : "outline"}
              onClick={() => setF({ transport: "stdio" })}
            >
              {uiText("stdio（本地）")}
            </Button>
            <Button
              type="button"
              variant={form.transport === "http" ? "default" : "outline"}
              onClick={() => setF({ transport: "http" })}
            >
              {uiText("http（远程）")}
            </Button>
            <Button
              type="button"
              variant={form.transport === "sse" ? "default" : "outline"}
              onClick={() => setF({ transport: "sse" })}
            >
              {uiText("sse（旧版）")}
            </Button>
          </div>
        </div>
        <div className="grid gap-2">
          <Label htmlFor="m-name">{uiText("名称")}</Label>
          <Input
            id="m-name"
            placeholder="filesystem"
            value={form.name}
            onChange={(e) => setF({ name: e.target.value })}
          />
        </div>
        {form.transport === "stdio" ? (
          <>
            <div className="grid gap-2">
              <Label htmlFor="m-cmd">{uiText("命令")}</Label>
              <Input
                id="m-cmd"
                className="font-mono"
                placeholder="npx"
                value={form.command}
                onChange={(e) => setF({ command: e.target.value })}
              />
            </div>
            <div className="grid gap-2">
              <Label htmlFor="m-args">{uiText("参数（JSON 字符串数组，保留空格）")}</Label>
              <Input
                id="m-args"
                className="font-mono"
                placeholder={'["-y", "@modelcontextprotocol/server-filesystem", "/data"]'}
                value={form.args}
                onChange={(e) => setF({ args: e.target.value })}
              />
            </div>
          </>
        ) : (
          <div className="grid gap-2">
            <Label htmlFor="m-url">{uiText("远程 URL")}</Label>
            <Input
              id="m-url"
              className="font-mono"
              placeholder="https://mcp.example.com/mcp"
              value={form.url}
              onChange={(e) => setF({ url: e.target.value })}
            />
            <label className="flex items-center gap-2 text-sm">
              <Checkbox checked={form.insecure} onCheckedChange={(v) => setF({ insecure: v === true })} />
              {uiText("跳过 TLS 证书校验（自签证书）")}
            </label>
          </div>
        )}
        <div className="grid gap-2">
          <Label htmlFor="m-env">
            {form.transport !== "stdio"
              ? uiText("请求头（每行 KEY=VALUE，如 Authorization=Bearer xxx）")
              : uiText("环境变量（每行 KEY=VALUE）")}
          </Label>
          <p className="text-muted-foreground text-xs">{uiText("留空保留已存凭据；输入新值将替换全部环境变量或请求头。")}</p>
          {editing && <p className="text-muted-foreground text-xs">{uiText("已存键名")}: {(editing.env_keys ?? Object.keys(editing.env ?? {})).join(", ") || "—"}</p>}
          {editing && <label className="flex items-center gap-2 text-sm"><Checkbox checked={form.clearEnv} onCheckedChange={(value) => setF({ clearEnv: value === true })} />{uiText("清空全部已存环境变量 / 请求头")}</label>}
          <Textarea
            id="m-env"
            disabled={form.clearEnv}
            className="font-mono"
            placeholder={form.transport !== "stdio" ? "Authorization=Bearer xxxx" : "API_KEY=xxxx\nFOO=bar"}
            value={form.env}
            onChange={(e) => setF({ env: e.target.value })}
          />
        </div>
      </fieldset>
    );
  }

  function renderTools() {
    return (
      <div className="flex flex-col gap-3 py-4">
        <div className="flex items-center justify-between">
          <span className="text-muted-foreground text-sm">
            {tools.length} {uiText("个工具")}
          </span>
          <Button size="sm" variant="outline" disabled={refreshing || !editing?.approved || !editing?.enabled || dirty} onClick={refreshTools}>
            <RefreshCwIcon className={refreshing ? "animate-spin" : ""} /> {uiText("刷新")}
          </Button>
        </div>
        <p className="text-muted-foreground text-xs">{uiText("工具列表为缓存；仅已授权、已启用且无未保存修改时可实时刷新。")}</p>
        {toolsLoading ? (
          <p className="text-muted-foreground text-sm">{uiText("加载中…")}</p>
        ) : tools.length === 0 ? (
          <p className="text-muted-foreground text-sm">{uiText("尚未发现工具，点击刷新重新获取。")}</p>
        ) : (
          <div className="flex flex-col divide-y">
            {tools.map((t) => (
              <div key={t.name} className="py-2.5">
                <code className="font-mono text-sm">{t.name}</code>
                {t.description && (
                  <p className="text-muted-foreground mt-0.5 text-xs leading-relaxed">{t.description}</p>
                )}
              </div>
            ))}
          </div>
        )}
      </div>
    );
  }

  return (
    <div className="flex flex-1 flex-col gap-4 md:gap-6">
      <div>
        <h1 className="text-xl font-semibold tracking-tight">MCP</h1>
        <p className="text-muted-foreground text-sm">{uiText("外部 MCP 工具服务器 · 按 Agent 授权可见")}</p>
      </div>

      <p className="text-muted-foreground text-sm">{uiText("保存不授权、不连接。升级前的配置需重新授权一次；授权后调用自动执行。")}</p>
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-3">
        <button
          type="button"
          onClick={openAdd}
          className="text-foreground/70 border-foreground/70 hover:bg-muted/60 hover:shadow-sm flex min-h-[116px] flex-col items-center justify-center gap-2 rounded-xl border border-dashed transition"
        >
          <PlusIcon className="size-6" />
          <span className="text-sm">{uiText("添加 MCP")}</span>
        </button>

        {servers.map((s) => (
          <Card
            key={s.id}
            onClick={() => openEdit(s)}
            className="hover:border-primary/60 cursor-pointer gap-3 transition hover:shadow-sm"
          >
            <CardHeader>
              <div className="flex items-center gap-2">
                <ServerIcon className="text-muted-foreground size-4 shrink-0" />
                <CardTitle className="truncate text-base">{s.name}</CardTitle>
                <Badge variant="outline" className="shrink-0 uppercase">
                  {s.transport}
                </Badge>
                <div className="ml-auto flex items-center gap-2" onClick={(e) => e.stopPropagation()}>
                  <Switch checked={s.enabled && s.approved} disabled={mutating || saving} onCheckedChange={() => s.enabled && s.approved ? mutateServer(s, false) : requestApproval(s)} aria-label={uiText("授权并启用")} />
                  <DeleteConfirmation name={s.name} description={uiText("服务器配置及其 Agent 可见性设置将被删除，后续工具装配将不再包含此服务器的工具。已运行的调用不保证立即停止。")} onConfirm={() => removeServer(s)}>
                  <Button size="icon" variant="outline" aria-label={uiText("删除")}>
                    <Trash2Icon className="text-destructive" />
                  </Button>
                  </DeleteConfirmation>
                </div>
              </div>
            </CardHeader>
            <CardContent className="grid gap-3">
              <Badge variant="outline">{s.approved ? (s.enabled ? uiText("已授权并启用") : uiText("已停用")) : uiText("待授权")}</Badge>
              <p className="text-muted-foreground text-sm">
                {s.tools && s.tools.length > 0 ? uiText("{v0} 个工具", { v0: s.tools.length }) : uiText("尚未发现工具")}
              </p>
              <div className="grid gap-2" onClick={(e) => e.stopPropagation()}>
                <span className="text-muted-foreground text-xs">{uiText("可见性（按 Agent 授权）")}</span>
                <div className="flex flex-wrap gap-x-4 gap-y-2">
                  {agents.map((a) => (
                    <label key={a.key} className="flex items-center gap-2 text-sm">
                      <Checkbox
                        checked={(visibility[s.id] ?? []).includes(a.id)}
                        onCheckedChange={() => toggleVisibility(s.id, a.id, a.name)}
                      />
                      {a.name}
                    </label>
                  ))}
                </div>
              </div>
            </CardContent>
          </Card>
        ))}
      </div>

      <AlertDialog open={!!approval} onOpenChange={(next) => { if (!next && !mutationLock.current) setApproval(null); }}>
        <AlertDialogContent aria-busy={mutating} onOpenAutoFocus={(event) => { event.preventDefault(); cancelApprovalRef.current?.focus(); }} onCloseAutoFocus={(event) => { event.preventDefault(); if (approvalLauncherRef.current?.isConnected) approvalLauncherRef.current.focus({ preventScroll: true }); }} onEscapeKeyDown={(event) => { if (mutationLock.current) event.preventDefault(); }}>
          <AlertDialogHeader>
            <AlertDialogTitle>{uiText("授权并启用当前持久化配置？")}</AlertDialogTitle>
            <AlertDialogDescription>{uiText("确认后允许连接并自动调用此版本的工具。未保存的输入不会获得授权。")}</AlertDialogDescription>
          </AlertDialogHeader>
          {approval && <div className="grid gap-2 overflow-auto text-sm">
            <p>{approval.name} · {approval.transport} · {uiText("配置版本")} {approval.revision}</p>
            <pre className="max-h-48 whitespace-pre-wrap break-all rounded bg-muted p-3">{mcpApprovalDestination(approval)}</pre>
            <p>{uiText("TLS 状态")}: {approval.transport === "stdio" ? uiText("不适用") : approval.url?.startsWith("https:") ? approval.insecure ? uiText("跳过证书校验") : uiText("校验证书") : uiText("明文 HTTP")}</p>
            <p>{uiText("环境变量 / 请求头键名（值不显示）")}: {(approval.env_keys ?? Object.keys(approval.env ?? {})).join(", ") || "—"}</p>
          </div>}
          <AlertDialogFooter>
            <AlertDialogCancel ref={cancelApprovalRef} disabled={mutating}>{uiText("取消")}</AlertDialogCancel>
            <AlertDialogAction disabled={mutating} onClick={(event) => { event.preventDefault(); if (approval) void mutateServer(approval, true); }}>{uiText("授权并启用")}</AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
      <Sheet open={open} onOpenChange={setOpen}>
        <SheetContent side="right" className="w-full data-[side=right]:sm:max-w-lg">
          <SheetHeader>
            <SheetTitle>{editing ? editing.name : uiText("添加 MCP 服务器")}</SheetTitle>
            <SheetDescription>{uiText("stdio（本地起进程）或 http（远程 Streamable HTTP）")}</SheetDescription>
          </SheetHeader>

          {editing ? (
            <Tabs
              value={tab}
              onValueChange={(v) => setTab(v as "config" | "tools")}
              className="flex min-h-0 flex-1 flex-col px-4"
            >
              <TabsList>
                <TabsTrigger value="config">{uiText("配置")}</TabsTrigger>
                <TabsTrigger value="tools">
                  {uiText("工具列表")}
                  {tools.length ? `（${tools.length}）` : ""}
                </TabsTrigger>
              </TabsList>
              <TabsContent value="config" className="min-h-0 flex-1 overflow-y-auto">
                {renderForm()}
                <div className="flex gap-2 pt-2 pb-6">
                  <Button onClick={saveForm} disabled={saving || mutating}>
                    {uiText("保存配置")}
                  </Button>
                  <Button variant="outline" disabled={saving || mutating || dirty} onClick={() => requestApproval(editing)}>{uiText("授权并启用")}</Button>
                </div>
              </TabsContent>
              <TabsContent value="tools" className="min-h-0 flex-1 overflow-y-auto">
                {renderTools()}
              </TabsContent>
            </Tabs>
          ) : (
            <div className="flex min-h-0 flex-1 flex-col overflow-y-auto px-4">
              {renderForm()}
              <div className="pt-2 pb-6">
                <Button onClick={saveForm} disabled={saving || mutating}>
                  <PlusIcon /> {uiText("添加")}
                </Button>
              </div>
            </div>
          )}
        </SheetContent>
      </Sheet>
    </div>
  );
}
