"use client";

import * as React from "react";

import Link from "next/link";

import {
  ArrowUpRightIcon,
  BugIcon,
  ChevronRightIcon,
  ClockIcon,
  DownloadIcon,
  InfoIcon,
  SearchIcon,
  ShieldAlertIcon,
  TriangleAlertIcon,
} from "lucide-react";
import { toast } from "sonner";

import { FindingCaseList } from "@/components/finding-case-list";
import { FindingRetestDialog } from "@/components/finding-retest-dialog";
import { StatusBadge } from "@/components/status-badge";
import { TablePagination } from "@/components/table-pagination";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Checkbox } from "@/components/ui/checkbox";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Field, FieldDescription, FieldGroup, FieldLabel } from "@/components/ui/field";
import { InputGroup, InputGroupAddon, InputGroupInput } from "@/components/ui/input-group";
import { RadioGroup, RadioGroupItem } from "@/components/ui/radio-group";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Spinner } from "@/components/ui/spinner";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Textarea } from "@/components/ui/textarea";
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group";
import { api } from "@/lib/api";
import { useI18n } from "@/lib/i18n";
import { getLocalStorageValue, setLocalStorageValue } from "@/lib/local-storage.client";
import { statusMeta } from "@/lib/status";
import type {
  ActiveFindingRetest,
  Finding,
  FindingAssetNode,
  FindingGroup,
  FindingStats,
  FindingStatus,
  Severity,
} from "@/lib/types";
import { cn } from "@/lib/utils";

import { AssetTree, assetPathOf } from "./_components/asset-tree";
import {
  FINDING_STATUSES,
  type FindingEdit,
  type FindingReport,
  FindingsTable,
  findingRowKey,
  fmtTime,
  isSameFinding,
  SEVERITIES,
  UNASSIGNED_TASK,
} from "./_components/findings-table";

const FINDING_EXPANSION_KEY = "artex_finding_task_expansion";
const FINDING_LIST_PREFERENCE_KEY = "artex_finding_list_preferences";

// 列表视图:flat = 跨任务平铺大表(默认);grouped = 按任务分组折叠;
// asset = 左侧资产树 + 右侧该子树下的发现。
type FindingView = "cases" | "flat" | "grouped" | "asset";

const FINDING_VIEWS: FindingView[] = ["cases", "flat", "grouped", "asset"];

// 资产树的一次性快照。与另外两个视图不同,资产视图不轮询:进入视图、改筛选、
// 或本页改动了发现之后才重新查询。
interface AssetTreeState {
  nodes: FindingAssetNode[];
  findingTotal: number;
  truncated: boolean;
  droppedKinds: string[];
  loaded: boolean;
  loading: boolean;
  queryKey: string | null;
  error: string | null;
}

const EMPTY_ASSET_TREE: AssetTreeState = {
  nodes: [],
  findingTotal: 0,
  truncated: false,
  droppedKinds: [],
  loaded: false,
  loading: false,
  queryKey: null,
  error: null,
};

interface FindingGroupsState {
  items: FindingGroup[];
  total: number;
  findingTotal: number;
  loaded: boolean;
  loading: boolean;
  queryKey: string | null;
  error: string | null;
}

const EMPTY_GROUPS_STATE: FindingGroupsState = {
  items: [],
  total: 0,
  findingTotal: 0,
  loaded: false,
  loading: false,
  queryKey: null,
  error: null,
};

// 分组视图里每个已展开任务组自带一份分页状态,彼此独立。
interface GroupFindingsState {
  items: Finding[];
  total: number;
  page: number;
  pageSize: number;
  loaded: boolean;
  loading: boolean;
  queryKey: string;
  error: string | null;
}

// 平铺视图的页码单独放 state(而非塞进快照),筛选一变就能连带重置并触发重新加载。
interface FlatFindingsState {
  items: Finding[];
  total: number;
  loaded: boolean;
  loading: boolean;
  queryKey: string | null;
  error: string | null;
}

const EMPTY_FLAT_STATE: FlatFindingsState = {
  items: [],
  total: 0,
  loaded: false,
  loading: false,
  queryKey: null,
  error: null,
};

function findingGroupKey(group: FindingGroup) {
  return group.task_id === null ? UNASSIGNED_TASK : String(group.task_id);
}

const EMPTY_STATS: FindingStats = {
  total: 0,
  pending: 0,
  critical: 0,
  high: 0,
  medium: 0,
  low: 0,
  vulnclasses: [],
  tasks: [],
};

export default function FindingsPage() {
  const { t: uiText } = useI18n();
  const [view, setView] = React.useState<FindingView>("cases");
  const [severity, setSeverity] = React.useState<"all" | Severity>("all");
  const [status, setStatus] = React.useState<"all" | FindingStatus>("all");
  const [vulnclass, setVulnclass] = React.useState<string>("all");
  const [task, setTask] = React.useState<string>("all");
  const [sort, setSort] = React.useState<"severity" | "time">("severity");
  const [search, setSearch] = React.useState("");
  const [query, setQuery] = React.useState("");
  const [expanded, setExpanded] = React.useState<string | null>(null);
  const [flat, setFlat] = React.useState<FlatFindingsState>(EMPTY_FLAT_STATE);
  const [flatPage, setFlatPage] = React.useState(1);
  const [flatPageSize, setFlatPageSize] = React.useState(20);
  const [assetTree, setAssetTree] = React.useState<AssetTreeState>(EMPTY_ASSET_TREE);
  const [assetScope, setAssetScope] = React.useState<string | null>(null);
  const [groupList, setGroupList] = React.useState<FindingGroupsState>(EMPTY_GROUPS_STATE);
  const [expandedGroups, setExpandedGroups] = React.useState<Set<string>>(() => new Set());
  const savedExpansions = React.useRef<Record<string, string[]>>({});
  const expansionRestorePending = React.useRef(false);
  const [groupFindings, setGroupFindings] = React.useState<Record<string, GroupFindingsState>>({});
  const [stats, setStats] = React.useState<FindingStats>(EMPTY_STATS);
  const [statsLoaded, setStatsLoaded] = React.useState(false);
  const [preferencesHydrated, setPreferencesHydrated] = React.useState(false);
  const [page, setPage] = React.useState(1);
  const [pageSize, setPageSize] = React.useState(10);
  const [deepenFinding, setDeepenFinding] = React.useState<Finding | null>(null);
  const [retestFinding, setRetestFinding] = React.useState<Finding | null>(null);
  const [activeRetests, setActiveRetests] = React.useState<Record<string, ActiveFindingRetest>>({});
  const activeRetestFingerprint = Object.values(activeRetests)
    .map((item) => item.id)
    .join(",");
  const retestRefreshVersion = React.useRef(0);
  const [deepenDescription, setDeepenDescription] = React.useState("");
  const [deepening, setDeepening] = React.useState(false);
  const filterFingerprint = JSON.stringify([severity, status, vulnclass, task, sort, query]);
  const activeFilterFingerprint = React.useRef(filterFingerprint);
  activeFilterFingerprint.current = filterFingerprint;
  const groupsQueryKey = JSON.stringify([view, filterFingerprint, page, pageSize]);
  const activeGroupsQueryKey = React.useRef(groupsQueryKey);
  activeGroupsQueryKey.current = groupsQueryKey;
  const activeView = React.useRef(view);
  activeView.current = view;
  const groups = groupList.queryKey === groupsQueryKey ? groupList.items : [];

  // 一个轻量请求覆盖所有行/视图，避免逐行拉取完整复测历史；等待上一轮完成再轮询。
  React.useEffect(() => {
    let disposed = false;
    let failed = false;
    let timer: ReturnType<typeof setTimeout>;
    async function refreshRetests() {
      const version = retestRefreshVersion.current;
      try {
        const rows = await api.activeFindingRetests();
        if (disposed || version !== retestRefreshVersion.current) return;
        setActiveRetests(Object.fromEntries(rows.map((item) => [item.finding_id, item])));
        failed = false;
      } catch (error) {
        if (!disposed && !failed) toast.error(uiText("加载复测状态失败：{v0}", { v0: (error as Error).message }));
        failed = true;
      } finally {
        if (!disposed) timer = setTimeout(() => void refreshRetests(), 3000);
      }
    }
    void refreshRetests();
    return () => {
      disposed = true;
      clearTimeout(timer);
    };
  }, [uiText]);

  React.useEffect(() => {
    try {
      const saved = JSON.parse(getLocalStorageValue(FINDING_EXPANSION_KEY) ?? "{}");
      if (saved && typeof saved === "object" && !Array.isArray(saved)) savedExpansions.current = saved;
    } catch {
      /* Ignore malformed expansion preferences. */
    }
    const raw = getLocalStorageValue(FINDING_LIST_PREFERENCE_KEY);
    if (raw) {
      try {
        const parsed = JSON.parse(raw) as {
          view?: unknown;
          severity?: unknown;
          status?: unknown;
          vulnclass?: unknown;
          task?: unknown;
          sort?: unknown;
          query?: unknown;
        };
        if (FINDING_VIEWS.includes(parsed.view as FindingView)) setView(parsed.view as FindingView);
        if (parsed.severity === "all" || SEVERITIES.includes(parsed.severity as Severity)) {
          setSeverity(parsed.severity as "all" | Severity);
        }
        if (parsed.status === "all" || FINDING_STATUSES.includes(parsed.status as FindingStatus)) {
          setStatus(parsed.status as "all" | FindingStatus);
        }
        if (typeof parsed.vulnclass === "string" && parsed.vulnclass) setVulnclass(parsed.vulnclass);
        if (typeof parsed.task === "string" && parsed.task) setTask(parsed.task);
        if (typeof parsed.query === "string") {
          setSearch(parsed.query);
          setQuery(parsed.query);
        }
        if (parsed.sort === "severity" || parsed.sort === "time") setSort(parsed.sort);
      } catch {
        // Ignore malformed or legacy preferences and retain the defaults.
      }
    }
    setPreferencesHydrated(true);
  }, []);

  React.useEffect(() => {
    if (!preferencesHydrated) return;
    setLocalStorageValue(
      FINDING_LIST_PREFERENCE_KEY,
      JSON.stringify({ view, severity, status, vulnclass, task, sort, query }),
    );
  }, [preferencesHydrated, severity, sort, status, task, view, vulnclass, query]);

  React.useEffect(() => {
    const timer = window.setTimeout(() => setQuery(search.trim()), 300);
    return () => window.clearTimeout(timer);
  }, [search]);

  // setFindings 同时改写两个视图缓存里的同一条发现,切换视图不会看到过期状态。
  const setFindings = React.useCallback((update: (current: Finding[]) => Finding[]) => {
    setFlat((current) => ({ ...current, items: update(current.items) }));
    setGroupFindings((current) => {
      const next: Record<string, GroupFindingsState> = {};
      for (const [key, state] of Object.entries(current)) {
        next[key] = { ...state, items: update(state.items) };
      }
      return next;
    });
  }, []);

  // 勾选导出:按 finding_id(独立表 id)记选中项,跨页保留。
  const [selectedIds, setSelectedIds] = React.useState<Set<string>>(() => new Set());
  // 导出弹窗状态:范围(当前筛选/全部/选中) × 格式(md 单文件/md 分文件 zip/csv/json)。
  const [exportOpen, setExportOpen] = React.useState(false);
  const [exportScope, setExportScope] = React.useState<"filtered" | "all" | "selected">("filtered");
  const [exportFormat, setExportFormat] = React.useState<"md-single" | "md-zip" | "csv" | "json">("md-single");
  const [includeOriginals, setIncludeOriginals] = React.useState(false);
  const [reviewing, setReviewing] = React.useState(false);
  const [exporting, setExporting] = React.useState(false);
  const [bulkBusy, setBulkBusy] = React.useState(false);
  const [bulkDeleteOpen, setBulkDeleteOpen] = React.useState(false);
  const [bulkFailures, setBulkFailures] = React.useState<string[]>([]);
  const [caseRefresh, setCaseRefresh] = React.useState(0);

  const toggleSelected = React.useCallback((id: string, checked: boolean) => {
    setSelectedIds((prev) => {
      const next = new Set(prev);
      if (checked) next.add(id);
      else next.delete(id);
      return next;
    });
  }, []);

  const toggleSelectedPage = React.useCallback((ids: string[], checked: boolean) => {
    setSelectedIds((prev) => {
      const next = new Set(prev);
      for (const id of ids) {
        if (checked) next.add(id);
        else next.delete(id);
      }
      return next;
    });
  }, []);

  // 打开导出弹窗时,若有勾选项则默认范围切到「选中」,否则「当前筛选」。
  function openExport() {
    setExportScope(selectedIds.size > 0 ? "selected" : "filtered");
    setExportOpen(true);
  }

  async function doExport() {
    setExporting(true);
    try {
      await api.exportFindings({
        format: exportFormat,
        scope: exportScope,
        filters: {
          severity,
          status,
          vulnclass,
          task,
          query,
          sort,
          assetScope: exportScope === "filtered" ? (activeAssetScope ?? undefined) : undefined,
        },
        ids: [...selectedIds],
        includeOriginals,
      });
      setExportOpen(false);
      toast.success(uiText("已开始下载导出文件"));
    } catch (e) {
      toast.error(uiText("导出失败：{v0}", { v0: (e as Error).message }));
    } finally {
      setExporting(false);
    }
  }

  const flatRequest = React.useRef(0);
  const flatInFlight = React.useRef<{ queryKey: string; request: number } | null>(null);
  const assetTreeRequest = React.useRef(0);
  const groupRequests = React.useRef<Record<string, number>>({});
  const groupInFlight = React.useRef<Record<string, { queryKey: string; request: number }>>({});
  const groupsRequest = React.useRef(0);
  const groupsInFlight = React.useRef<{ queryKey: string; request: number } | null>(null);
  const expandedGroupsRef = React.useRef(expandedGroups);
  const groupFindingsRef = React.useRef(groupFindings);
  const visibleGroupKeysRef = React.useRef<Set<string>>(new Set());
  expandedGroupsRef.current = expandedGroups;
  groupFindingsRef.current = groupFindings;
  visibleGroupKeysRef.current = new Set(groups.map(findingGroupKey));

  // 资产视图右侧列表 = 平铺列表 + 选中子树的筛选,所以两个视图共用一份列表状态。
  const activeAssetScope = view === "asset" ? assetScope : null;
  const flatQueryKey = JSON.stringify([view, filterFingerprint, activeAssetScope, flatPage, flatPageSize]);
  const activeFlatQueryKey = React.useRef(flatQueryKey);
  activeFlatQueryKey.current = flatQueryKey;

  // loadFlat 拉取平铺视图的当前页;task 筛选交给后端,与分组视图共用同一批筛选条件。
  // 导航与行内改动立即刷新;只有定时轮询才跳过同一查询的在途请求。
  const loadFlat = React.useCallback(
    async (force = true) => {
      const requestQueryKey = flatQueryKey;
      if (activeFlatQueryKey.current !== requestQueryKey) return;
      if (!force && flatInFlight.current?.queryKey === requestQueryKey) return;
      const request = ++flatRequest.current;
      flatInFlight.current = { queryKey: requestQueryKey, request };
      setFlat((current) => ({
        ...(current.queryKey === requestQueryKey ? current : EMPTY_FLAT_STATE),
        queryKey: requestQueryKey,
        loading: true,
        error: null,
      }));
      try {
        const result = await api.findingsPage({
          page: flatPage,
          pageSize: flatPageSize,
          severity,
          status,
          vulnclass,
          task,
          query,
          sort,
          assetScope: activeAssetScope ?? undefined,
        });
        if (request !== flatRequest.current || activeFlatQueryKey.current !== requestQueryKey) return;
        setFlat({
          items: result.items,
          total: result.total,
          loaded: true,
          loading: false,
          queryKey: requestQueryKey,
          error: null,
        });
      } catch (error) {
        if (request !== flatRequest.current || activeFlatQueryKey.current !== requestQueryKey) return;
        // Polling keeps the last successful snapshot visible.
        setFlat((current) => ({
          ...current,
          loading: false,
          error: error instanceof Error ? error.message : uiText("请检查连接后重试"),
        }));
      } finally {
        if (flatInFlight.current?.request === request) flatInFlight.current = null;
      }
    },
    [activeAssetScope, flatQueryKey, flatPage, flatPageSize, severity, status, vulnclass, task, query, sort, uiText],
  );

  // loadAssetTree 取整棵资产树。树不随选中节点变化(否则选一下就塌成一条链),
  // 所以这里不带 assetScope。
  const loadAssetTree = React.useCallback(async () => {
    const requestFilter = filterFingerprint;
    if (activeFilterFingerprint.current !== requestFilter) return;
    const request = ++assetTreeRequest.current;
    setAssetTree((current) => ({
      ...(current.queryKey === requestFilter ? current : EMPTY_ASSET_TREE),
      queryKey: requestFilter,
      loading: true,
      error: null,
    }));
    try {
      const result = await api.findingAssetTree({ severity, status, vulnclass, task, query, sort });
      if (request !== assetTreeRequest.current || activeFilterFingerprint.current !== requestFilter) return;
      setAssetTree({
        nodes: result.nodes ?? [],
        findingTotal: result.finding_total ?? 0,
        truncated: Boolean(result.truncated),
        droppedKinds: result.dropped_kinds ?? [],
        loaded: true,
        loading: false,
        queryKey: requestFilter,
        error: null,
      });
    } catch (e) {
      if (request !== assetTreeRequest.current || activeFilterFingerprint.current !== requestFilter) return;
      setAssetTree((current) => ({
        ...current,
        loading: false,
        error: e instanceof Error ? e.message : uiText("请检查连接后重试"),
      }));
    }
  }, [filterFingerprint, severity, status, vulnclass, task, query, sort, uiText]);

  const refreshGroups = React.useCallback(
    async (force = true) => {
      const requestQueryKey = groupsQueryKey;
      if (activeGroupsQueryKey.current !== requestQueryKey || activeView.current !== "grouped") return;
      if (!force && groupsInFlight.current?.queryKey === requestQueryKey) return;
      const request = ++groupsRequest.current;
      groupsInFlight.current = { queryKey: requestQueryKey, request };
      setGroupList((current) => ({
        ...(current.queryKey === requestQueryKey ? current : EMPTY_GROUPS_STATE),
        queryKey: requestQueryKey,
        loading: true,
        error: null,
      }));
      try {
        const result = await api.findingGroups({
          page,
          pageSize,
          severity,
          status,
          vulnclass,
          task,
          query,
          sort,
        });
        if (request !== groupsRequest.current || activeGroupsQueryKey.current !== requestQueryKey) return;
        setGroupList({
          items: result.items,
          total: result.total,
          findingTotal: result.finding_total,
          loaded: true,
          loading: false,
          queryKey: requestQueryKey,
          error: null,
        });
      } catch (error) {
        if (request !== groupsRequest.current || activeGroupsQueryKey.current !== requestQueryKey) return;
        setGroupList((current) => ({
          ...current,
          loading: false,
          error: error instanceof Error ? error.message : uiText("请检查连接后重试"),
        }));
      } finally {
        if (groupsInFlight.current?.request === request) groupsInFlight.current = null;
      }
    },
    [groupsQueryKey, page, pageSize, severity, status, vulnclass, task, query, sort, uiText],
  );

  const loadGroup = React.useCallback(
    async (key: string, groupPage: number, groupPageSize: number, force = true) => {
      const requestFilter = filterFingerprint;
      const requestQueryKey = JSON.stringify([requestFilter, key, groupPage, groupPageSize]);
      if (activeFilterFingerprint.current !== requestFilter || activeView.current !== "grouped") return;
      if (!force && groupInFlight.current[key]?.queryKey === requestQueryKey) return;
      const request = (groupRequests.current[key] ?? 0) + 1;
      groupRequests.current[key] = request;
      groupInFlight.current[key] = { queryKey: requestQueryKey, request };
      setGroupFindings((current) => ({
        ...current,
        [key]: {
          items: current[key]?.queryKey === requestQueryKey ? current[key].items : [],
          total: current[key]?.queryKey === requestQueryKey ? current[key].total : 0,
          page: groupPage,
          pageSize: groupPageSize,
          loaded: current[key]?.queryKey === requestQueryKey && current[key].loaded,
          loading: true,
          queryKey: requestQueryKey,
          error: null,
        },
      }));
      try {
        const result = await api.findingsPage({
          page: groupPage,
          pageSize: groupPageSize,
          severity,
          status,
          vulnclass,
          task: key,
          query,
          sort,
        });
        if (groupRequests.current[key] !== request || activeFilterFingerprint.current !== requestFilter) return;
        setGroupFindings((current) => ({
          ...current,
          [key]: {
            items: result.items,
            total: result.total,
            page: groupPage,
            pageSize: groupPageSize,
            loaded: true,
            loading: false,
            queryKey: requestQueryKey,
            error: null,
          },
        }));
      } catch (error) {
        if (groupRequests.current[key] !== request || activeFilterFingerprint.current !== requestFilter) return;
        setGroupFindings((current) => ({
          ...current,
          [key]: {
            ...(current[key] ?? {
              items: [],
              total: 0,
              page: groupPage,
              pageSize: groupPageSize,
              loaded: false,
              queryKey: requestQueryKey,
            }),
            loading: false,
            error: error instanceof Error ? error.message : uiText("请检查连接后重试"),
          },
        }));
      } finally {
        if (groupInFlight.current[key]?.request === request) delete groupInFlight.current[key];
      }
    },
    [filterFingerprint, severity, status, vulnclass, query, sort, uiText],
  );

  React.useEffect(() => {
    for (const [key, state] of Object.entries(groupFindings)) {
      if (
        !state.loaded ||
        state.loading ||
        state.error ||
        state.queryKey !== JSON.stringify([filterFingerprint, key, state.page, state.pageSize])
      )
        continue;
      const lastPage = Math.max(1, Math.ceil(state.total / state.pageSize));
      if (state.page > lastPage) void loadGroup(key, lastPage, state.pageSize);
    }
  }, [filterFingerprint, groupFindings, loadGroup]);

  const toggleGroup = React.useCallback(
    (key: string) => {
      const opening = !expandedGroups.has(key);
      const next = new Set(expandedGroups);
      if (opening) next.add(key);
      else next.delete(key);
      setExpandedGroups(next);
      const state = groupFindings[key];
      if (opening && !state?.loaded && !state?.loading) {
        void loadGroup(key, state?.page ?? 1, state?.pageSize ?? 10);
      }
    },
    [expandedGroups, groupFindings, loadGroup],
  );

  // 行内改动后刷新当前视图:平铺视图重拉当前页,分组视图刷组头 + 该发现所在的组。
  // 写操作完成时用户可能已换视图/资产/页码,因此使用最近一轮渲染的刷新函数。
  const mutationRefreshRef = React.useRef({ view, loadFlat, loadAssetTree, refreshGroups, loadGroup });
  mutationRefreshRef.current = { view, loadFlat, loadAssetTree, refreshGroups, loadGroup };
  const refreshAfterMutation = React.useCallback((finding: Finding, removed = false) => {
    const { view, loadFlat, loadAssetTree, refreshGroups, loadGroup } = mutationRefreshRef.current;
    if (view === "asset") {
      // 资产视图不轮询,所以改完要顺带把树的计数也重新算一次。
      void loadFlat();
      void loadAssetTree();
      return;
    }
    if (view === "flat") {
      // 删空最后一页时,页码由越界修正 effect 回退并连带重新加载。
      void loadFlat();
      return;
    }
    void refreshGroups();
    const key = finding.task_id ?? UNASSIGNED_TASK;
    const state = groupFindingsRef.current[key];
    if (state) {
      const nextTotal = Math.max(0, state.total - (removed ? 1 : 0));
      const lastPage = Math.max(1, Math.ceil(nextTotal / state.pageSize));
      const nextPage = state.loaded ? Math.min(state.page, lastPage) : state.page;
      void loadGroup(key, nextPage, state.pageSize);
    }
  }, []);

  async function mutateSelected(nextStatus?: FindingStatus) {
    setBulkBusy(true);
    setBulkFailures([]);
    const succeeded = new Set<string>();
    const failures: string[] = [];
    for (const id of [...selectedIds]) {
      try {
        if (nextStatus) await api.setFindingStatus(id, nextStatus);
        else await api.deleteFinding(id);
        succeeded.add(id);
      } catch (error) {
        failures.push(`#${id}: ${(error as Error).message}`);
      }
    }
    setSelectedIds((current) => new Set([...current].filter((id) => !succeeded.has(id))));
    setBulkFailures(failures);
    setFindings((current) =>
      nextStatus
        ? current.map((finding) =>
            succeeded.has(finding.finding_id ?? finding.id) ? { ...finding, status: nextStatus } : finding,
          )
        : current.filter((finding) => !succeeded.has(finding.finding_id ?? finding.id)),
    );
    const refresh = mutationRefreshRef.current;
    if (refresh.view === "asset") {
      void refresh.loadAssetTree();
      void refresh.loadFlat(true);
    } else if (refresh.view === "flat") void refresh.loadFlat(true);
    else if (refresh.view === "grouped") {
      void refresh.refreshGroups(true);
      for (const key of expandedGroupsRef.current) {
        const state = groupFindingsRef.current[key];
        if (state) void refresh.loadGroup(key, state.page, state.pageSize, true);
      }
    }
    setCaseRefresh((current) => current + 1);
    void api
      .findingStats()
      .then(setStats)
      .catch(() => {
        /* The successful mutations remain valid if totals cannot refresh. */
      });
    if (succeeded.size) toast.success(uiText("已处理 {v0} 条", { v0: succeeded.size }));
    setBulkDeleteOpen(false);
    setBulkBusy(false);
  }

  // Reset every view's pagination and expansion when a shared finding filter changes.
  React.useEffect(() => {
    void filterFingerprint;
    // Invalidate requests even if the user returns to an earlier filter before they settle.
    for (const key of Object.keys(groupRequests.current)) groupRequests.current[key]++;
    groupInFlight.current = {};
    assetTreeRequest.current++;
    setPage(1);
    setExpanded(null);
    const saved = savedExpansions.current[filterFingerprint];
    expansionRestorePending.current = true;
    setExpandedGroups(
      new Set(Array.isArray(saved) ? saved.filter((key) => typeof key === "string") : task !== "all" ? [task] : []),
    );
    setGroupFindings({});
    setFlatPage(1);
    setFlat(EMPTY_FLAT_STATE);
    // 筛选变了树也会变,原先选中的节点可能已经不在树里,退回「全部资产」。
    setAssetScope(null);
    setAssetTree(EMPTY_ASSET_TREE);
  }, [filterFingerprint, task]);

  React.useEffect(() => {
    if (!preferencesHydrated) return;
    if (expansionRestorePending.current) {
      expansionRestorePending.current = false;
      return;
    }
    savedExpansions.current[filterFingerprint] = [...expandedGroups];
    setLocalStorageValue(FINDING_EXPANSION_KEY, JSON.stringify(savedExpansions.current));
  }, [expandedGroups, filterFingerprint, preferencesHydrated]);

  React.useEffect(() => {
    if (!preferencesHydrated || view !== "grouped" || groupList.queryKey !== groupsQueryKey) return;
    for (const key of expandedGroups) {
      if (!groups.some((group) => findingGroupKey(group) === key)) continue;
      const state = groupFindings[key];
      if (!state?.loaded && !state?.loading && !state?.error)
        void loadGroup(key, state?.page ?? 1, state?.pageSize ?? 10);
    }
  }, [expandedGroups, groupFindings, groupList.queryKey, groupsQueryKey, groups, loadGroup, preferencesHydrated, view]);

  // 换资产节点等于换了一份结果集,回到第一页。
  React.useEffect(() => {
    void assetScope;
    setFlatPage(1);
  }, [assetScope]);

  // 资产树只在进入视图 / 筛选变化时查一次(以及本页改动发现后由
  // refreshAfterMutation 主动重拉),不做轮询。
  React.useEffect(() => {
    if (!preferencesHydrated || view !== "asset") return;
    void activeRetestFingerprint; // 复测结束可能改变状态筛选下的资产计数。
    void loadAssetTree();
  }, [activeRetestFingerprint, loadAssetTree, preferencesHydrated, view]);

  // 只轮询当前视图:平铺视图刷当前页,分组视图刷组头与每个已展开的组(其分页彼此独立)。
  // 资产视图只查一次(见下面的 return),它的左树是导航结构,没必要每 5 秒重算。
  // 等偏好水合后再发首个请求,否则会先按默认视图/筛选白拉一次。
  React.useEffect(() => {
    if (!preferencesHydrated) return;
    void activeRetestFingerprint; // 包括不定时轮询的资产视图，也在复测结束后刷新处置状态。
    const refresh = (force = false) => {
      if (view === "flat" || view === "asset") {
        void loadFlat(force);
        return;
      }
      void refreshGroups(force);
      for (const key of expandedGroupsRef.current) {
        if (!visibleGroupKeysRef.current.has(key)) continue;
        const state = groupFindingsRef.current[key];
        if (state?.loaded && !state.loading) void loadGroup(key, state.page, state.pageSize, force);
      }
    };
    refresh(true);
    if (view === "asset") return;
    const timer = setInterval(refresh, 5000);
    return () => clearInterval(timer);
  }, [activeRetestFingerprint, loadFlat, loadGroup, preferencesHydrated, refreshGroups, view]);

  React.useEffect(() => {
    if (!groupList.loaded || groupList.queryKey !== groupsQueryKey) return;
    const lastPage = Math.max(1, Math.ceil(groupList.total / pageSize));
    if (page > lastPage) setPage(lastPage);
  }, [groupList.loaded, groupList.queryKey, groupList.total, groupsQueryKey, page, pageSize]);

  React.useEffect(() => {
    if (!flat.loaded || flat.queryKey !== flatQueryKey) return;
    const lastPage = Math.max(1, Math.ceil(flat.total / flatPageSize));
    if (flatPage > lastPage) setFlatPage(lastPage);
  }, [flat.loaded, flat.queryKey, flat.total, flatPage, flatPageSize, flatQueryKey]);

  // Whole-table aggregates (stat cards + vuln-class options) — independent of the
  // current page, so they stay exact.
  React.useEffect(() => {
    let alive = true;
    const load = () => {
      api
        .findingStats()
        .then((s) => {
          if (alive) {
            setStats(s);
            setStatsLoaded(true);
          }
        })
        .catch(() => {
          // Keep the previous aggregate snapshot until the next poll.
        });
    };
    load();
    const t = setInterval(load, 5000);
    return () => {
      alive = false;
      clearInterval(t);
    };
  }, []);

  React.useEffect(() => {
    if (!statsLoaded) return;
    if (vulnclass !== "all" && !stats.vulnclasses.includes(vulnclass)) setVulnclass("all");
    if (
      task !== "all" &&
      task !== UNASSIGNED_TASK &&
      !(stats.tasks ?? []).some((option) => String(option.id) === task)
    ) {
      setTask("all");
    }
  }, [stats, statsLoaded, task, vulnclass]);

  // updateStatus optimistically flips one finding's triage state, reverting on error.
  const updateStatus = React.useCallback(
    async (f: Finding, next: FindingStatus) => {
      if (!f.finding_id || next === f.status) return;
      const prev = f.status;
      setFindings((cur) => cur.map((x) => (isSameFinding(x, f) ? { ...x, status: next } : x)));
      try {
        await api.setFindingStatus(f.finding_id, next);
        toast.success(uiText("已标记为「{v0}」", { v0: uiText(statusMeta("finding", next).label) }));
        // refresh stat cards (pending count) and drop the row if it no longer matches the status filter
        api
          .findingStats()
          .then(setStats)
          .catch(() => {
            // The row update remains valid even if the aggregate refresh fails.
          });
        if (status !== "all" && next !== status) {
          setFindings((cur) => cur.filter((x) => !isSameFinding(x, f)));
          setGroupList((current) => ({ ...current, findingTotal: Math.max(0, current.findingTotal - 1) }));
          setFlat((cur) => ({ ...cur, total: Math.max(0, cur.total - 1) }));
        }
        refreshAfterMutation(f);
      } catch (e) {
        setFindings((cur) => cur.map((x) => (isSameFinding(x, f) ? { ...x, status: prev } : x)));
        toast.error(uiText("更新失败：{v0}", { v0: (e as Error).message }));
      }
    },
    [refreshAfterMutation, setFindings, status, uiText],
  );

  // 行内展开的详细报告缓存按全局稳定行键存。report 是大段 Markdown,列表查询不带它,
  // 故展开时才按 finding_id 单独拉取一次;done 且文本为空 = 该漏洞暂无报告。
  const [reports, setReports] = React.useState<Record<string, FindingReport>>({});

  // 行内可编辑缓冲:当前展开行的名称/类别/严重等级,展开时用该行数据初始化,收起清空。
  // 单行展开,故一份缓冲即可。
  const [edit, setEdit] = React.useState<FindingEdit | null>(null);
  const [saving, setSaving] = React.useState(false);

  // toggle 展开/收起一行;新展开时初始化编辑缓冲,并(尚未取过时)按 finding_id 拉一次报告缓存。
  const toggleRow = React.useCallback(
    (f: Finding) => {
      const key = findingRowKey(f);
      const willOpen = expanded !== key;
      setExpanded(willOpen ? key : null);
      if (!willOpen) {
        setEdit(null);
        return;
      }
      setEdit({ name: f.name ?? "", vulnclass: f.vulnclass, severity: f.severity });
      if (!f.finding_id || reports[key]) return;
      const fid = f.finding_id;
      setReports((r) => ({ ...r, [key]: { status: "loading", text: "" } }));
      api
        .getFinding(fid)
        .then((full) => setReports((r) => ({ ...r, [key]: { status: "done", text: full.report ?? "" } })))
        .catch(() => setReports((r) => ({ ...r, [key]: { status: "error", text: "" } })));
    },
    [expanded, reports],
  );

  // saveEdit 保存当前展开行的名称/类别/严重等级,回写本地列表并刷新统计(类别下拉/严重计数可能变)。
  const saveEdit = React.useCallback(
    async (f: Finding) => {
      if (!f.finding_id || !edit) return;
      setSaving(true);
      try {
        const updated = await api.updateFinding(f.finding_id, {
          name: edit.name.trim(),
          vulnclass: edit.vulnclass.trim(),
          severity: edit.severity,
        });
        setFindings((cur) =>
          cur.map((x) =>
            isSameFinding(x, f)
              ? { ...x, name: updated.name, vulnclass: updated.vulnclass, severity: updated.severity }
              : x,
          ),
        );
        toast.success(uiText("已保存"));
        api
          .findingStats()
          .then(setStats)
          .catch(() => {
            // The edit remains valid even if the aggregate refresh fails.
          });
        refreshAfterMutation(f);
      } catch (e) {
        toast.error(uiText("保存失败：{v0}", { v0: (e as Error).message }));
      } finally {
        setSaving(false);
      }
    },
    [edit, refreshAfterMutation, setFindings, uiText],
  );

  // deleteFinding 删除一个漏洞(需二次确认):删成功后从列表移除、收起行、刷新统计。
  const deleteFinding = React.useCallback(
    async (f: Finding) => {
      if (!f.finding_id) return;
      try {
        await api.deleteFinding(f.finding_id);
        // 删除期间可能已换查询。只扣当前快照里仍显示的行,其余计数由刷新结果更新。
        setFlat((current) => {
          if (
            current.queryKey !== activeFlatQueryKey.current ||
            !current.items.some((item) => isSameFinding(item, f))
          ) {
            return current;
          }
          return { ...current, total: Math.max(0, current.total - 1) };
        });
        setFindings((cur) => cur.filter((x) => !isSameFinding(x, f)));
        setSelectedIds((current) => {
          const next = new Set(current);
          next.delete(f.finding_id as string);
          return next;
        });
        setGroupList((current) => ({ ...current, findingTotal: Math.max(0, current.findingTotal - 1) }));
        const rowKey = findingRowKey(f);
        setExpanded((cur) => (cur === rowKey ? null : cur));
        toast.success(uiText("已删除漏洞"));
        api
          .findingStats()
          .then(setStats)
          .catch(() => {
            // The deletion remains valid even if the aggregate refresh fails.
          });
        refreshAfterMutation(f, true);
      } catch (e) {
        toast.error(uiText("删除失败：{v0}", { v0: (e as Error).message }));
      }
    },
    [refreshAfterMutation, setFindings, uiText],
  );

  const openDeepen = React.useCallback((f: Finding) => {
    setDeepenFinding(f);
    setDeepenDescription("");
  }, []);

  async function submitDeepen() {
    if (!deepenFinding?.finding_id || !deepenDescription.trim() || deepening) return;
    setDeepening(true);
    try {
      const result = await api.deepenFinding(deepenFinding.finding_id, deepenDescription.trim());
      toast.success(
        result.queued
          ? uiText("深入意图 #{v0} 已进入任务队列", { v0: result.intent_id })
          : uiText("已创建高优先级 Worker 意图 #{v0}", { v0: result.intent_id }),
      );
      refreshAfterMutation(deepenFinding);
      setDeepenFinding(null);
      setDeepenDescription("");
    } catch (error) {
      toast.error(uiText("提交失败：{v0}", { v0: (error as Error).message }));
    } finally {
      setDeepening(false);
    }
  }

  const statCards = [
    { label: uiText("独立漏洞"), value: stats.distinct?.total ?? stats.total, icon: BugIcon },
    { label: uiText("上报记录"), value: stats.total, icon: ClockIcon },
    { label: uiText("待处理"), value: stats.pending, tone: "text-amber-800 dark:text-amber-400", icon: ClockIcon },
    {
      label: uiText("严重"),
      value: stats.distinct?.critical ?? stats.critical,
      tone: "text-rose-700 dark:text-rose-400",
      icon: ShieldAlertIcon,
    },
    {
      label: uiText("高危"),
      value: stats.distinct?.high ?? stats.high,
      tone: "text-red-700 dark:text-red-400",
      icon: TriangleAlertIcon,
    },
    {
      label: uiText("中危"),
      value: stats.distinct?.medium ?? stats.medium,
      tone: "text-amber-800 dark:text-amber-400",
      icon: TriangleAlertIcon,
    },
    {
      label: uiText("低危"),
      value: stats.distinct?.low ?? stats.low,
      tone: "text-slate-600 dark:text-slate-400",
      icon: InfoIcon,
    },
  ];

  // 导出弹窗里「当前筛选」的条数:两个视图的筛选一致,只是统计口径来源不同。
  // 平铺与资产视图共用 flat 列表状态,分组视图的口径来自组接口的 finding_total。
  const groupedFindingTotal = groupList.queryKey === groupsQueryKey ? groupList.findingTotal : 0;
  const [caseTotal, setCaseTotal] = React.useState(0);
  const filteredTotal = view === "cases" ? caseTotal : view === "grouped" ? groupedFindingTotal : flat.total;
  const assetPath = React.useMemo(
    () =>
      view === "asset" && assetTree.queryKey === filterFingerprint ? assetPathOf(assetTree.nodes, assetScope) : [],
    [assetScope, assetTree.nodes, assetTree.queryKey, filterFingerprint, view],
  );

  const rowProps = {
    selectedIds,
    onToggleSelected: toggleSelected,
    onToggleSelectedPage: toggleSelectedPage,
    expandedKey: expanded,
    onToggleRow: toggleRow,
    reports,
    edit,
    onEditChange: setEdit,
    saving,
    onSave: saveEdit,
    onStatusChange: updateStatus,
    onRetest: setRetestFinding,
    activeRetests,
    onDeepen: openDeepen,
    onDelete: deleteFinding,
  };

  // 平铺视图与资产视图右侧是同一张表 + 同一份分页,只是筛选条件不同。
  const flatListCard = (
    <Card className="min-w-0 gap-0 overflow-hidden py-0" aria-busy={flat.loading}>
      <CardContent className="px-0">
        {flat.queryKey !== flatQueryKey || (flat.loading && !flat.loaded) ? (
          <div className="flex min-h-36 items-center justify-center gap-2 text-muted-foreground text-sm" role="status">
            <Spinner aria-hidden="true" />
            {uiText("正在加载发现…")}
          </div>
        ) : flat.error && !flat.loaded ? (
          <div className="flex min-h-36 flex-col items-center justify-center gap-3 p-6 text-center" role="alert">
            <p className="font-medium">{uiText("发现加载失败")}</p>
            <p className="text-muted-foreground text-sm">{flat.error}</p>
            <Button size="sm" variant="outline" onClick={() => void loadFlat()}>
              {uiText("重新加载")}
            </Button>
          </div>
        ) : (
          <>
            {flat.error && (
              <div
                className="flex flex-wrap items-center justify-between gap-2 border-b px-4 py-3 text-sm"
                role="status"
              >
                <span>{uiText("更新失败，正在显示上次结果。")}</span>
                <Button size="sm" variant="outline" onClick={() => void loadFlat()}>
                  {uiText("重试")}
                </Button>
              </div>
            )}
            <div className="flex flex-wrap items-center justify-between gap-2 border-b px-4 py-3 text-muted-foreground text-xs">
              <span role="status" aria-live="polite">
                {uiText("当前筛选共")}{" "}
                <strong className="font-medium text-foreground tabular-nums">{flat.total}</strong> {uiText("条发现")}
              </span>
              {flat.items.length > 0 && (
                <span className={view === "asset" ? undefined : "lg:hidden"}>
                  {uiText("横向滚动查看资产、状态与操作")}
                </span>
              )}
            </div>
            <FindingsTable items={flat.items} selectAllLabel={uiText("选择当前页全部")} {...rowProps} />
            <TablePagination
              page={flatPage}
              pageSize={flatPageSize}
              total={flat.total}
              onPageChange={setFlatPage}
              onPageSizeChange={(nextSize) => {
                setFlatPageSize(nextSize);
                setFlatPage(1);
              }}
              pageSizeOptions={[10, 20, 50, 100]}
            />
          </>
        )}
      </CardContent>
    </Card>
  );

  return (
    <div className="flex min-w-0 flex-1 flex-col gap-4 md:gap-6">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h1 className="font-semibold text-2xl tracking-tight md:text-3xl">{uiText("发现")}</h1>
          <p className="text-muted-foreground text-sm">{uiText("跨任务漏洞汇总")}</p>
        </div>
        <Tabs value={view} onValueChange={(v) => setView(v as FindingView)}>
          <TabsList>
            <TabsTrigger value="cases">{uiText("按漏洞")}</TabsTrigger>
            <TabsTrigger value="flat">{uiText("原始上报")}</TabsTrigger>
            <TabsTrigger value="grouped">{uiText("按任务分组")}</TabsTrigger>
            <TabsTrigger value="asset">{uiText("按资产")}</TabsTrigger>
          </TabsList>
        </Tabs>
      </div>
      <div className="flex flex-1 flex-col gap-4 md:gap-6">
        <div className="grid grid-cols-3 gap-2 sm:gap-3 lg:grid-cols-6">
          {statCards.map((stat) => {
            const StatIcon = stat.icon;
            return (
              <Card key={stat.label} className="gap-1 py-3 sm:py-4">
                <CardHeader className="gap-1 px-3 sm:px-4">
                  <CardDescription className="text-xs sm:text-sm">{stat.label}</CardDescription>
                  <CardTitle className={cn("flex items-center gap-1.5 text-xl tabular-nums sm:text-2xl", stat.tone)}>
                    <StatIcon className="size-4 shrink-0 sm:size-5" aria-hidden="true" />
                    {statsLoaded ? stat.value : "—"}
                  </CardTitle>
                </CardHeader>
              </Card>
            );
          })}
        </div>

        <fieldset
          className="flex min-w-0 flex-wrap items-center gap-2 rounded-xl border bg-card p-3 sm:p-4"
          aria-label={uiText("发现筛选")}
        >
          <InputGroup className="w-full sm:w-72">
            <InputGroupInput
              type="search"
              value={search}
              onChange={(event) => setSearch(event.target.value)}
              placeholder={uiText("检索漏洞内容")}
              aria-label={uiText("检索漏洞内容")}
            />
            <InputGroupAddon>
              <SearchIcon aria-hidden="true" />
            </InputGroupAddon>
          </InputGroup>

          <ToggleGroup
            type="single"
            value={severity}
            onValueChange={(value) => value && setSeverity(value as "all" | Severity)}
            variant="outline"
            size="sm"
            spacing={0}
          >
            {(
              [
                ["all", uiText("全部")],
                ["critical", uiText("严重")],
                ["high", uiText("高危")],
                ["medium", uiText("中危")],
                ["low", uiText("低危")],
              ] as const
            ).map(([val, label]) => (
              <ToggleGroupItem key={val} value={val} aria-label={uiText("按{v0}等级筛选", { v0: label })}>
                {label}
              </ToggleGroupItem>
            ))}
          </ToggleGroup>

          <Select value={status} onValueChange={(v) => setStatus(v as "all" | FindingStatus)}>
            <SelectTrigger size="sm" className="h-8 w-32" aria-label={uiText("按状态筛选")}>
              <SelectValue placeholder={uiText("状态")} />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="all">{uiText("全部状态")}</SelectItem>
              {FINDING_STATUSES.map((st) => (
                <SelectItem key={st} value={st}>
                  {uiText(statusMeta("finding", st).label)}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>

          <Select value={vulnclass} onValueChange={setVulnclass}>
            <SelectTrigger size="sm" className="h-8 w-36 sm:w-40" aria-label={uiText("按漏洞类型筛选")}>
              <SelectValue placeholder={uiText("漏洞类型")} />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="all">{uiText("全部类型")}</SelectItem>
              {stats.vulnclasses.map((vc) => (
                <SelectItem key={vc} value={vc}>
                  {vc}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>

          <Select value={task} onValueChange={setTask}>
            <SelectTrigger size="sm" className="h-8 w-48" aria-label={uiText("按任务筛选")}>
              <SelectValue placeholder={uiText("任务")} />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="all">{uiText("全部任务")}</SelectItem>
              <SelectItem value={UNASSIGNED_TASK}>{uiText("未关联 / 任务已删除")}</SelectItem>
              {(stats.tasks ?? []).map((t) => {
                const id = String(t.id);
                const label = t.name || t.description || uiText("任务 #{v0}（已删除）", { v0: id });
                return (
                  <SelectItem key={id} value={id}>
                    <span className="flex w-full items-center gap-2">
                      <span className="max-w-[14rem] truncate" title={label}>
                        {label}
                      </span>
                      <span className="inline-flex items-center gap-1 text-muted-foreground tabular-nums">
                        <BugIcon className="size-3.5" aria-hidden="true" />
                        {t.count}
                      </span>
                    </span>
                  </SelectItem>
                );
              })}
            </SelectContent>
          </Select>

          <Select value={sort} onValueChange={(v) => setSort(v as "severity" | "time")}>
            <SelectTrigger size="sm" className="h-8 w-36" aria-label={uiText("发现排序")}>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="severity">{uiText("按严重度")}</SelectItem>
              <SelectItem value="time">{uiText("按时间")}</SelectItem>
            </SelectContent>
          </Select>

          <div className="ml-auto flex flex-wrap items-center gap-3">
            {selectedIds.size > 0 ? (
              <>
                <Select
                  value=""
                  disabled={bulkBusy}
                  onValueChange={(value) => {
                    if (value) void mutateSelected(value as FindingStatus);
                  }}
                >
                  <SelectTrigger size="sm" className="h-8 w-32" aria-label={uiText("批量修改状态")}>
                    <SelectValue placeholder={uiText("修改所选状态")} />
                  </SelectTrigger>
                  <SelectContent>
                    {FINDING_STATUSES.map((value) => (
                      <SelectItem key={value} value={value}>
                        {uiText(statusMeta("finding", value).label)}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
                <Button size="sm" variant="destructive" disabled={bulkBusy} onClick={() => setBulkDeleteOpen(true)}>
                  {uiText("删除所选")}
                </Button>
              </>
            ) : null}
            {selectedIds.size > 0 && (
              <span className="text-muted-foreground text-xs tabular-nums">
                {uiText("已选")} {selectedIds.size} {uiText("条")}
              </span>
            )}
            <Button
              size="sm"
              variant="outline"
              disabled={reviewing || selectedIds.size === 0}
              onClick={async () => {
                setReviewing(true);
                try {
                  await api.reviewFindingCases([...selectedIds]);
                  toast.success(uiText("已提交整理，按任务分别执行"));
                } catch (e) {
                  toast.error((e as Error).message);
                } finally {
                  setReviewing(false);
                }
              }}
            >
              {reviewing ? <Spinner /> : null}
              {uiText("整理所选（{count}）", { count: selectedIds.size })}
            </Button>
            <Button size="sm" variant="outline" onClick={openExport}>
              <DownloadIcon /> {uiText("导出")}
            </Button>
          </div>
        </fieldset>

        {bulkFailures.length > 0 ? (
          <div role="alert" className="rounded-md border border-destructive p-3 text-sm">
            <p>{uiText("部分操作失败，失败记录仍保持选中：")}</p>
            {bulkFailures.map((failure) => (
              <p key={failure}>{failure}</p>
            ))}
          </div>
        ) : null}
        {view === "flat" && flatListCard}

        {view === "asset" && (
          <div className="grid min-h-0 items-start gap-4 lg:grid-cols-[20rem_minmax(0,1fr)] xl:grid-cols-[24rem_minmax(0,1fr)]">
            <Card className="gap-0 py-3 lg:sticky lg:top-4">
              <CardContent className="flex flex-col px-3">
                {(assetTree.queryKey !== filterFingerprint || (!assetTree.loaded && !assetTree.error)) && (
                  <div
                    className="flex min-h-36 items-center justify-center gap-2 text-muted-foreground text-sm"
                    role="status"
                  >
                    <Spinner aria-hidden="true" />
                    {uiText("正在加载资产树…")}
                  </div>
                )}
                {assetTree.queryKey === filterFingerprint && assetTree.error && !assetTree.loaded && (
                  <div
                    className="flex min-h-36 flex-col items-center justify-center gap-3 p-3 text-center"
                    role="alert"
                  >
                    <p className="font-medium">{uiText("资产树加载失败")}</p>
                    <p className="text-muted-foreground text-sm">{assetTree.error}</p>
                    <Button size="sm" variant="outline" onClick={() => void loadAssetTree()}>
                      {uiText("重新加载资产树")}
                    </Button>
                  </div>
                )}
                {assetTree.queryKey === filterFingerprint && assetTree.loaded && (
                  <>
                    {assetTree.error && (
                      <div className="flex flex-col items-start gap-2 border-b pb-3 text-sm" role="status">
                        <span>{uiText("资产树更新失败，正在显示上次结果。")}</span>
                        <Button size="sm" variant="outline" onClick={() => void loadAssetTree()}>
                          {uiText("重试资产树")}
                        </Button>
                      </div>
                    )}
                    <AssetTree
                      nodes={assetTree.nodes}
                      selected={assetScope}
                      onSelect={setAssetScope}
                      loading={assetTree.loading}
                      truncated={assetTree.truncated}
                      droppedKinds={assetTree.droppedKinds}
                      findingTotal={assetTree.findingTotal}
                      onRefresh={() => void loadAssetTree()}
                    />
                  </>
                )}
              </CardContent>
            </Card>
            <div className="flex min-w-0 flex-col gap-2">
              <div className="flex min-w-0 flex-wrap items-center gap-1 text-muted-foreground text-sm">
                <button
                  type="button"
                  className={cn("hover:text-foreground", assetScope === null && "font-medium text-foreground")}
                  onClick={() => setAssetScope(null)}
                >
                  {uiText("全部资产")}
                </button>
                {assetPath.map((node) => (
                  <React.Fragment key={node.key}>
                    <ChevronRightIcon className="size-3.5 shrink-0" aria-hidden="true" />
                    <button
                      type="button"
                      className={cn(
                        "max-w-[16rem] truncate hover:text-foreground",
                        node.key === assetScope && "font-medium text-foreground",
                      )}
                      title={node.label}
                      onClick={() => setAssetScope(node.key)}
                    >
                      {node.display}
                    </button>
                  </React.Fragment>
                ))}
                <span className="ml-auto shrink-0 text-xs tabular-nums">
                  {uiText("共")} {flat.total} {uiText("条")}
                </span>
              </div>
              {flatListCard}
            </div>
          </div>
        )}

        {view === "cases" && (
          <FindingCaseList
            onTotal={setCaseTotal}
            refreshToken={caseRefresh}
            query={{ severity, status, vulnclass, task, query, sort }}
            selectedIds={selectedIds}
            onSelect={toggleSelected}
          />
        )}
        {view === "grouped" && (
          <div className="flex flex-col gap-3">
            {(groupList.queryKey !== groupsQueryKey || (!groupList.loaded && !groupList.error)) && (
              <Card>
                <CardContent
                  className="flex min-h-36 items-center justify-center gap-2 text-muted-foreground text-sm"
                  role="status"
                >
                  <Spinner aria-hidden="true" />
                  {uiText("正在加载任务分组…")}
                </CardContent>
              </Card>
            )}
            {groupList.queryKey === groupsQueryKey && groupList.error && !groupList.loaded && (
              <Card>
                <CardContent
                  className="flex min-h-36 flex-col items-center justify-center gap-3 p-6 text-center"
                  role="alert"
                >
                  <p className="font-medium">{uiText("任务分组加载失败")}</p>
                  <p className="text-muted-foreground text-sm">{groupList.error}</p>
                  <Button size="sm" variant="outline" onClick={() => void refreshGroups()}>
                    {uiText("重新加载")}
                  </Button>
                </CardContent>
              </Card>
            )}
            {groupList.queryKey === groupsQueryKey && groupList.error && groupList.loaded && (
              <div
                className="flex flex-wrap items-center justify-between gap-2 rounded-lg border px-4 py-3 text-sm"
                role="status"
              >
                <span>{uiText("任务分组更新失败，正在显示上次结果。")}</span>
                <Button size="sm" variant="outline" onClick={() => void refreshGroups()}>
                  {uiText("重试任务分组")}
                </Button>
              </div>
            )}
            {groups.map((group) => {
              const key = findingGroupKey(group);
              const groupOpen = expandedGroups.has(key);
              const state = groupFindings[key] ?? {
                items: [],
                total: group.count,
                page: 1,
                pageSize: 10,
                loaded: false,
                loading: false,
                queryKey: JSON.stringify([filterFingerprint, key, 1, 10]),
                error: null,
              };
              return (
                <Card key={key} className="gap-0 py-0">
                  <CardHeader className="px-4 py-3">
                    <div className="flex min-w-0 flex-wrap items-center gap-3">
                      <button
                        type="button"
                        className="flex min-w-0 flex-1 items-center gap-3 text-left"
                        aria-expanded={groupOpen}
                        onClick={() => toggleGroup(key)}
                      >
                        <ChevronRightIcon
                          className={cn(
                            "size-4 shrink-0 text-muted-foreground transition-transform",
                            groupOpen && "rotate-90",
                          )}
                        />
                        <div className="flex min-w-0 flex-col gap-1">
                          <CardTitle className="truncate text-sm">
                            {group.task_id === null
                              ? uiText("未关联 / 任务已删除")
                              : group.task_name
                                ? uiText("{v0}（任务 #{v1}）", { v0: group.task_name, v1: group.task_id })
                                : uiText("任务 #{v0}", { v0: group.task_id })}
                          </CardTitle>
                          <CardDescription className="truncate" title={group.task_description}>
                            {group.task_description || uiText("来源任务不可用")}
                          </CardDescription>
                        </div>
                      </button>
                      <div className="flex flex-wrap items-center gap-2">
                        {group.task_status && <StatusBadge domain="task" value={group.task_status} dot />}
                        {SEVERITIES.map((level) => {
                          const count = group[level];
                          if (count === 0) return null;
                          return (
                            <span key={level} className="inline-flex items-center gap-1">
                              <StatusBadge domain="severity" value={level} dot />
                              <span className="text-muted-foreground text-xs tabular-nums">{count}</span>
                            </span>
                          );
                        })}
                        <span className="text-muted-foreground text-xs tabular-nums">
                          {fmtTime(group.last_found_at)}
                        </span>
                        {group.task_id !== null && (
                          <Button size="icon-sm" variant="ghost" asChild>
                            <Link
                              href={`/function/tasks/detail?id=${group.task_id}`}
                              aria-label={uiText("查看任务 #{v0}", { v0: group.task_id })}
                            >
                              <ArrowUpRightIcon />
                            </Link>
                          </Button>
                        )}
                      </div>
                    </div>
                  </CardHeader>
                  {groupOpen && (
                    <CardContent className="px-0" aria-busy={state.loading}>
                      {!state.loaded && !state.error && (
                        <div
                          className="flex min-h-36 items-center justify-center gap-2 text-muted-foreground text-sm"
                          role="status"
                        >
                          <Spinner aria-hidden="true" />
                          {uiText("正在加载本组发现…")}
                        </div>
                      )}
                      {state.error && !state.loaded && (
                        <div
                          className="flex min-h-36 flex-col items-center justify-center gap-3 p-6 text-center"
                          role="alert"
                        >
                          <p className="font-medium">{uiText("本组发现加载失败")}</p>
                          <p className="text-muted-foreground text-sm">{state.error}</p>
                          <Button
                            size="sm"
                            variant="outline"
                            onClick={() => void loadGroup(key, state.page, state.pageSize)}
                          >
                            {uiText("重新加载")}
                          </Button>
                        </div>
                      )}
                      {state.loaded && (
                        <>
                          {state.error && (
                            <div
                              className="flex flex-wrap items-center justify-between gap-2 border-b px-4 py-3 text-sm"
                              role="status"
                            >
                              <span>{uiText("本组发现更新失败，正在显示上次结果。")}</span>
                              <Button
                                size="sm"
                                variant="outline"
                                onClick={() => void loadGroup(key, state.page, state.pageSize)}
                              >
                                {uiText("重试本组发现")}
                              </Button>
                            </div>
                          )}
                          <FindingsTable
                            items={state.items}
                            selectAllLabel={uiText("选择本组当前页全部")}
                            {...rowProps}
                          />
                          <TablePagination
                            page={state.page}
                            pageSize={state.pageSize}
                            total={state.total}
                            onPageChange={(nextPage) => void loadGroup(key, nextPage, state.pageSize)}
                            onPageSizeChange={(nextSize) => void loadGroup(key, 1, nextSize)}
                          />
                        </>
                      )}
                    </CardContent>
                  )}
                </Card>
              );
            })}
            {groupList.queryKey === groupsQueryKey && groupList.loaded && groups.length === 0 && (
              <Card>
                <CardContent className="py-12 text-center text-muted-foreground text-sm">
                  {uiText("没有匹配的发现。")}
                </CardContent>
              </Card>
            )}
            {groupList.queryKey === groupsQueryKey && groupList.loaded && (
              <TablePagination
                page={page}
                pageSize={pageSize}
                total={groupList.total}
                onPageChange={setPage}
                onPageSizeChange={(nextPageSize) => {
                  setPageSize(nextPageSize);
                  setPage(1);
                }}
                pageSizeOptions={[5, 10, 20]}
              />
            )}
          </div>
        )}
      </div>

      {retestFinding?.finding_id ? (
        <FindingRetestDialog
          key={retestFinding.finding_id}
          findingId={retestFinding.finding_id}
          findingName={retestFinding.name || retestFinding.vulnclass || retestFinding.summary}
          onStarted={(retest) => {
            const findingId = retestFinding.finding_id;
            if (!findingId || retest.conversation_id == null || !["pending", "running"].includes(retest.status)) return;
            retestRefreshVersion.current++;
            const active: ActiveFindingRetest = {
              id: retest.id,
              finding_id: findingId,
              conversation_id: retest.conversation_id,
              status: retest.status === "pending" ? "pending" : "running",
            };
            setActiveRetests((current) => ({ ...current, [findingId]: active }));
          }}
          onClose={() => setRetestFinding(null)}
        />
      ) : null}

      <Dialog
        open={deepenFinding !== null}
        onOpenChange={(open) => {
          if (open || deepening) return;
          setDeepenFinding(null);
          setDeepenDescription("");
        }}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{uiText("深入利用漏洞")}</DialogTitle>
            <DialogDescription className="break-words">
              {uiText("将在原任务 #")}
              {deepenFinding?.task_id} {uiText("中创建优先级 10 的 Worker 意图，基于当前漏洞开展二次验证：")}
              {deepenFinding?.name || deepenFinding?.vulnclass || deepenFinding?.summary}
            </DialogDescription>
          </DialogHeader>
          <FieldGroup>
            <Field>
              <FieldLabel htmlFor="finding-deepen-description">{uiText("利用描述")}</FieldLabel>
              <Textarea
                id="finding-deepen-description"
                value={deepenDescription}
                onChange={(event) => setDeepenDescription(event.target.value)}
                maxLength={4000}
                placeholder={uiText("描述需要验证的利用路径、边界条件、目标或期望证据")}
                disabled={deepening}
              />
              <FieldDescription className="flex justify-between gap-3">
                <span>{uiText("新意图会继承该漏洞的资产锚点。")}</span>
                <span className="shrink-0 tabular-nums">{deepenDescription.length} / 4000</span>
              </FieldDescription>
            </Field>
          </FieldGroup>
          <DialogFooter>
            <Button
              variant="outline"
              onClick={() => {
                setDeepenFinding(null);
                setDeepenDescription("");
              }}
              disabled={deepening}
            >
              {uiText("取消")}
            </Button>
            <Button onClick={submitDeepen} disabled={deepening || !deepenDescription.trim()}>
              {deepening && <Spinner data-icon="inline-start" />}
              {uiText("创建深入意图")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog
        open={bulkDeleteOpen}
        onOpenChange={(open) => {
          if (!bulkBusy) setBulkDeleteOpen(open);
        }}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>
              {uiText("删除所选")} {selectedIds.size} {uiText("条发现？")}
            </DialogTitle>
            <DialogDescription>{uiText("删除发现和来源探索节点，此操作无法撤销。")}</DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" disabled={bulkBusy} onClick={() => setBulkDeleteOpen(false)}>
              {uiText("取消")}
            </Button>
            <Button variant="destructive" disabled={bulkBusy} onClick={() => void mutateSelected()}>
              {bulkBusy ? <Spinner /> : null}
              {uiText("删除")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
      <Dialog open={exportOpen} onOpenChange={setExportOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{uiText("导出发现")}</DialogTitle>
            <DialogDescription>{uiText("选择导出范围与格式,生成后浏览器会自动下载。")}</DialogDescription>
          </DialogHeader>

          <div className="flex flex-col gap-5 py-1">
            <div className="flex flex-col gap-2">
              <span className="text-muted-foreground text-xs">{uiText("导出范围")}</span>
              <RadioGroup value={exportScope} onValueChange={(v) => setExportScope(v as typeof exportScope)}>
                <label htmlFor="export-scope-filtered" className="flex items-center gap-2 text-sm">
                  <RadioGroupItem id="export-scope-filtered" value="filtered" /> {uiText("导出当前筛选结果（共")}{" "}
                  {filteredTotal} {uiText("条）")}
                </label>
                <label htmlFor="export-scope-all" className="flex items-center gap-2 text-sm">
                  <RadioGroupItem id="export-scope-all" value="all" /> {uiText("导出全部")}
                </label>
                <label
                  htmlFor="export-scope-selected"
                  className={cn("flex items-center gap-2 text-sm", selectedIds.size === 0 && "text-muted-foreground")}
                >
                  <RadioGroupItem id="export-scope-selected" value="selected" disabled={selectedIds.size === 0} />
                  {uiText("导出勾选的")} {selectedIds.size} {uiText("条")}
                </label>
              </RadioGroup>
            </div>

            <div className="flex flex-col gap-2">
              <span className="text-muted-foreground text-xs">{uiText("导出格式")}</span>
              <div className="flex items-center gap-2">
                <Checkbox
                  id="include-original-reports"
                  checked={includeOriginals}
                  onCheckedChange={(v) => setIncludeOriginals(v === true)}
                />
                <label htmlFor="include-original-reports" className="text-sm">
                  {uiText("包含原始子报告（默认每个文件夹一份统一报告）")}
                </label>
              </div>
              <RadioGroup value={exportFormat} onValueChange={(v) => setExportFormat(v as typeof exportFormat)}>
                <label htmlFor="export-format-md-single" className="flex items-center gap-2 text-sm">
                  <RadioGroupItem id="export-format-md-single" value="md-single" />{" "}
                  {uiText("Markdown 汇总报告（单个 .md 文件）")}
                </label>
                <label htmlFor="export-format-md-zip" className="flex items-center gap-2 text-sm">
                  <RadioGroupItem id="export-format-md-zip" value="md-zip" />{" "}
                  {uiText("Markdown 分文件（一漏洞一 .md,打包 .zip）")}
                </label>
                <label htmlFor="export-format-csv" className="flex items-center gap-2 text-sm">
                  <RadioGroupItem id="export-format-csv" value="csv" /> {uiText("CSV 表格（.csv）")}
                </label>
                <label htmlFor="export-format-json" className="flex items-center gap-2 text-sm">
                  <RadioGroupItem id="export-format-json" value="json" /> JSON（.json）
                </label>
              </RadioGroup>
            </div>
          </div>

          <DialogFooter>
            <Button variant="outline" onClick={() => setExportOpen(false)} disabled={exporting}>
              {uiText("取消")}
            </Button>
            <Button onClick={doExport} disabled={exporting || (exportScope === "selected" && selectedIds.size === 0)}>
              <DownloadIcon /> {exporting ? uiText("导出中…") : uiText("导出")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}
