'use client';

import { useCallback, useEffect, useMemo, useState } from 'react';
import { Activity, Bot, Check, ChevronRight, Clock3, History, LayoutDashboard, ListTodo, Loader2, MessageSquare, Pause, Play, Plus, RefreshCw, Settings2, ShieldCheck, Sparkles, Tags, Zap } from 'lucide-react';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card';
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle, DialogTrigger } from '@/components/ui/dialog';
import { Input } from '@/components/ui/input';
import { Label as FieldLabel } from '@/components/ui/label';
import { Progress } from '@/components/ui/progress';
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select';
import { Sidebar, SidebarContent, SidebarFooter, SidebarGroup, SidebarGroupContent, SidebarGroupLabel, SidebarHeader, SidebarInset, SidebarMenu, SidebarMenuButton, SidebarMenuItem, SidebarProvider, SidebarTrigger } from '@/components/ui/sidebar';
import { Switch } from '@/components/ui/switch';
import { Textarea } from '@/components/ui/textarea';

const API = 'http://127.0.0.1:8787/api';
type View = 'overview' | 'queue' | 'labels' | 'history' | 'settings';
type Provider = 'auto' | 'codex' | 'claude';
type TaskStatus = 'staged' | 'queued' | 'running' | 'waiting_user' | 'paused' | 'completed' | 'failed' | 'archived';
type Task = { id: string; title: string; project_path: string; provider_preference: Provider; provider_used: string; label_id: string; status: TaskStatus; priority: number; session_id: string; last_error: string; updated_at: string };
type Message = { id: number; role: 'user' | 'assistant'; content: string; created_at: string; dispatched_at?: string };
type TaskLabel = { id: string; name: string; emoji: string; color: string; codex_section: string; default_provider: Provider; min_window_remaining_pct: number; weekly_reserve_pct: number };
type Quota = { provider: 'codex' | 'claude'; connected: boolean; five_hour_remaining_pct: number; five_hour_reset_at: string; weekly_remaining_pct: number; error?: string };
type StatusResponse = { scheduler_enabled: boolean; main_work_active: boolean; quotas: Quota[] };
type WebTool = { name: string; title: string; description: string; inputSchema: Record<string, unknown>; annotations: { readOnlyHint: boolean; untrustedContentHint: boolean }; execute: (input: unknown) => unknown | Promise<unknown> };
declare global { interface Document { modelContext?: { registerTool: (tool: WebTool, options?: { signal?: AbortSignal }) => void | Promise<void> } } }

const providerName: Record<Provider, string> = { auto: '自动选择', codex: '仅 Codex', claude: '仅 Claude' };
const statusName: Record<TaskStatus, string> = { staged: '草稿', queued: '等待收割窗口', running: '正在执行', waiting_user: '等待你的回复', paused: '已暂停', completed: '已完成', failed: '运行失败', archived: '已归档' };
const nav = [
  { id: 'overview' as const, label: '概览', icon: LayoutDashboard },
  { id: 'queue' as const, label: '任务与会话', icon: ListTodo },
  { id: 'labels' as const, label: '标签', icon: Tags },
  { id: 'history' as const, label: '运行记录', icon: History },
  { id: 'settings' as const, label: '设置', icon: Settings2 },
];

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const response = await fetch(`${API}${path}`, { ...init, headers: { 'Content-Type': 'application/json', ...(init?.headers ?? {}) } });
  const body = await response.json().catch(() => ({})) as { error?: string };
  if (!response.ok) throw new Error(body.error || `请求失败（${response.status}）`);
  return body as T;
}

function LabelPill({ label }: { label?: TaskLabel }) {
  if (!label) return null;
  return <span className="inline-flex items-center gap-1.5 rounded-full border px-2.5 py-1 text-sm font-medium" style={{ borderColor: `${label.color}55`, background: `${label.color}12`, color: label.color }}>{label.emoji} {label.name}</span>;
}

function ProviderBadge({ provider }: { provider: string }) {
  const style = provider === 'codex' ? 'border-emerald-300 bg-emerald-50 text-emerald-700' : provider === 'claude' ? 'border-orange-300 bg-orange-50 text-orange-700' : 'border-sky-300 bg-sky-50 text-sky-700';
  return <Badge className={style}>{providerName[provider as Provider] ?? provider}</Badge>;
}

function ProviderSelect({ value, onChange }: { value: Provider; onChange: (value: Provider) => void }) {
  return <Select value={value} onValueChange={(value) => onChange(value as Provider)}><SelectTrigger className="w-full"><SelectValue /></SelectTrigger><SelectContent><SelectItem value="auto">自动选择有额度的一方</SelectItem><SelectItem value="codex">仅 Codex</SelectItem><SelectItem value="claude">仅 Claude</SelectItem></SelectContent></Select>;
}

export default function Home() {
  const [view, setView] = useState<View>('overview');
  const [online, setOnline] = useState(false);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [scheduler, setSchedulerState] = useState(false);
  const [focusLock, setFocusLockState] = useState(false);
  const [tasks, setTasks] = useState<Task[]>([]);
  const [labels, setLabels] = useState<TaskLabel[]>([]);
  const [quotas, setQuotas] = useState<Quota[]>([]);
  const [addOpen, setAddOpen] = useState(false);
  const [activeTask, setActiveTask] = useState<Task>();
  const [selectedLabel, setSelectedLabel] = useState('side');

  const refresh = useCallback(async () => {
    try {
      const [status, taskResult, labelResult] = await Promise.all([request<StatusResponse>('/status'), request<{ tasks: Task[] }>('/tasks'), request<{ labels: TaskLabel[] }>('/labels')]);
      setSchedulerState(status.scheduler_enabled); setFocusLockState(status.main_work_active); setQuotas(status.quotas ?? []); setTasks(taskResult.tasks ?? []); setLabels(labelResult.labels ?? []); setOnline(true); setError('');
    } catch (caught) { setOnline(false); setError(caught instanceof Error ? caught.message : '无法连接本地服务'); }
    finally { setLoading(false); }
  }, []);

  useEffect(() => { void refresh(); const timer = window.setInterval(() => void refresh(), 15_000); return () => window.clearInterval(timer); }, [refresh]);
  useEffect(() => {
    const context = document.modelContext; if (!context?.registerTool) return; const lifecycle = new AbortController();
    void context.registerTool({ name: 'list_staged_side_tasks', title: '查看待调度任务', description: '读取本地调度台中的持久任务。', inputSchema: { type: 'object', properties: {}, additionalProperties: false }, annotations: { readOnlyHint: true, untrustedContentHint: false }, execute: () => request('/tasks') }, { signal: lifecycle.signal });
    void context.registerTool({ name: 'stage_side_task', title: '暂存副业任务', description: '加入队列但不立即调用模型。', inputSchema: { type: 'object', properties: { title: { type: 'string' }, prompt: { type: 'string' }, project_path: { type: 'string' }, provider_preference: { type: 'string', enum: ['auto', 'codex', 'claude'] } }, required: ['title', 'prompt', 'project_path'], additionalProperties: false }, annotations: { readOnlyHint: false, untrustedContentHint: false }, execute: (input) => request('/tasks', { method: 'POST', body: JSON.stringify(input) }) }, { signal: lifecycle.signal });
    return () => lifecycle.abort();
  }, []);

  async function setting(key: string, value: boolean) { await request(`/settings/${key}`, { method: 'PUT', body: JSON.stringify({ value }) }); }
  async function setScheduler(value: boolean) { setSchedulerState(value); await setting('scheduler_enabled', value); }
  async function setFocusLock(value: boolean) { setFocusLockState(value); await setting('main_work_active', value); }
  const queued = useMemo(() => tasks.filter((task) => task.status === 'queued'), [tasks]);
  const currentLabel = labels.find((label) => label.id === selectedLabel) ?? labels[0];
  const pageTitle = nav.find((item) => item.id === view)?.label ?? '概览';

  return <SidebarProvider><Sidebar variant="inset" collapsible="icon" className="border-r-0">
    <SidebarHeader className="p-4"><div className="flex items-center gap-3 px-1"><div className="flex size-9 items-center justify-center rounded-xl bg-slate-950 text-white"><Zap className="size-4" fill="currentColor" /></div><div className="group-data-[collapsible=icon]:hidden"><p className="text-sm font-semibold">SpareRun</p><p className="text-xs text-slate-500">额度收割调度台</p></div></div></SidebarHeader>
    <SidebarContent><SidebarGroup><SidebarGroupLabel>工作区</SidebarGroupLabel><SidebarGroupContent><SidebarMenu>{nav.map((item) => <SidebarMenuItem key={item.id}><SidebarMenuButton isActive={view === item.id} tooltip={item.label} onClick={() => setView(item.id)}><item.icon /><span>{item.label}</span></SidebarMenuButton></SidebarMenuItem>)}</SidebarMenu></SidebarGroupContent></SidebarGroup></SidebarContent>
    <SidebarFooter className="p-4"><div className="rounded-xl border border-slate-200 bg-white p-3 group-data-[collapsible=icon]:hidden"><div className="flex items-center justify-between"><span className="text-sm font-medium">自动执行</span><Switch checked={scheduler} onCheckedChange={(value) => void setScheduler(value)} disabled={!online} /></div><p className="mt-2 text-xs text-slate-500">{scheduler ? '等待即将刷新的额度' : '默认关闭，不消耗额度'}</p></div></SidebarFooter>
  </Sidebar><SidebarInset className="min-h-svh bg-[#f5f7fb]"><header className="sticky top-0 z-20 flex h-16 items-center justify-between border-b border-slate-200/80 bg-[#f5f7fb]/90 px-4 backdrop-blur-xl sm:px-7"><div className="flex items-center gap-3"><SidebarTrigger /><div><h1 className="text-lg font-semibold">{pageTitle}</h1><p className="hidden text-xs text-slate-500 sm:block">主业优先，只用即将过期的额度</p></div></div><div className="flex items-center gap-2"><Button variant="outline" size="sm" onClick={() => void request('/refresh', { method: 'POST' })} disabled={!online}><RefreshCw />刷新额度</Button><Button variant={focusLock ? 'default' : 'outline'} size="sm" onClick={() => void setFocusLock(!focusLock)} disabled={!online} className={focusLock ? 'bg-rose-600 hover:bg-rose-700' : ''}><ShieldCheck /><span className="hidden sm:inline">{focusLock ? '主业保护中' : '开启主业保护'}</span></Button><AddTask open={addOpen} onOpenChange={setAddOpen} labels={labels} online={online} onCreated={refresh} /></div></header>
    <main className="mx-auto w-full max-w-[1480px] p-4 sm:p-7">{!online && <div className="mb-5 rounded-xl border border-amber-200 bg-amber-50 px-4 py-3 text-sm text-amber-800">本地后端未连接：{error}。界面不会伪造额度或执行任务。</div>}{loading ? <div className="flex min-h-64 items-center justify-center text-slate-500"><Loader2 className="mr-2 animate-spin" />正在连接本地服务</div> : <>{view === 'overview' && <Overview scheduler={scheduler} focusLock={focusLock} tasks={tasks} queued={queued} labels={labels} quotas={quotas} openQueue={() => setView('queue')} />}{view === 'queue' && <Queue tasks={tasks} labels={labels} onOpen={setActiveTask} refresh={refresh} />}{view === 'labels' && currentLabel && <Labels labels={labels} selected={currentLabel} select={setSelectedLabel} refresh={refresh} />}{view === 'history' && <HistoryView tasks={tasks} />}{view === 'settings' && <Settings scheduler={scheduler} setScheduler={setScheduler} quotas={quotas} />}</>}</main>
  </SidebarInset><Conversation task={activeTask} open={Boolean(activeTask)} close={() => setActiveTask(undefined)} refresh={refresh} /></SidebarProvider>;
}

function Overview({ scheduler, focusLock, tasks, queued, labels, quotas, openQueue }: { scheduler: boolean; focusLock: boolean; tasks: Task[]; queued: Task[]; labels: TaskLabel[]; quotas: Quota[]; openQueue: () => void }) {
  const next = queued[0]; return <div className="space-y-6"><section className="grid gap-4 xl:grid-cols-[1fr_1fr_.86fr]"><QuotaCard name="Codex" accent="#10b981" icon={<Bot className="size-5" />} quota={quotas.find((q) => q.provider === 'codex')} /><QuotaCard name="Claude" accent="#f97316" icon={<Sparkles className="size-5" />} quota={quotas.find((q) => q.provider === 'claude')} /><Card className="border-0 bg-slate-950 text-white"><CardHeader><div className="flex items-center justify-between text-slate-300"><span className="flex items-center gap-2 text-sm"><Activity className="size-4" />调度状态</span><span className={`size-2.5 rounded-full ${scheduler ? 'bg-emerald-400' : 'bg-slate-600'}`} /></div></CardHeader><CardContent><p className="text-2xl font-semibold">{!scheduler ? '安全暂停' : focusLock ? '保护主业中' : '等待收割窗口'}</p><p className="mt-2 text-sm text-slate-400">一轮结束后保留会话，等待你回复。</p><div className="mt-7 grid grid-cols-2 gap-3"><Metric value={queued.length} label="等待执行" /><Metric value={tasks.filter((t) => t.status === 'waiting_user').length} label="等待回复" /></div></CardContent></Card></section><section className="grid gap-5 lg:grid-cols-[1.35fr_.65fr]"><Card className="border-0 bg-white ring-1 ring-slate-200"><CardHeader className="flex flex-row items-center justify-between"><div><CardTitle className="text-base">下一项候选任务</CardTitle><p className="mt-1 text-sm text-slate-500">加入队列不会立即启动</p></div><Button variant="ghost" size="sm" onClick={openQueue}>查看队列 <ChevronRight /></Button></CardHeader><CardContent>{next ? <TaskSummary task={next} label={labels.find((label) => label.id === next.label_id)} /> : <Empty text="暂时没有排队任务" />}</CardContent></Card><Card className="border-0 bg-white ring-1 ring-slate-200"><CardHeader><CardTitle className="text-base">启动检查</CardTitle></CardHeader><CardContent className="space-y-4"><CheckRow text="自动执行已开启" pass={scheduler} /><CheckRow text="主业处于空闲" pass={!focusLock} /><CheckRow text="存在排队任务" pass={queued.length > 0} /><CheckRow text="临近五小时刷新" pass={quotas.some((q) => q.connected && new Date(q.five_hour_reset_at).getTime() - Date.now() <= 45 * 60_000)} /><CheckRow text="周额度高于保留线" pass={quotas.some((q) => q.weekly_remaining_pct >= 20)} /></CardContent></Card></section></div>;
}

function QuotaCard({ name, accent, icon, quota }: { name: string; accent: string; icon: React.ReactNode; quota?: Quota }) {
  return <Card className="border-0 bg-white ring-1 ring-slate-200"><CardHeader className="flex flex-row items-center justify-between pb-3"><div className="flex items-center gap-3"><div className="flex size-10 items-center justify-center rounded-xl" style={{ color: accent, background: `${accent}18` }}>{icon}</div><div><CardTitle className="text-base">{name}</CardTitle><p className="text-sm text-slate-500">{quota?.connected ? '已连接' : '暂不可用'}</p></div></div><span className={`size-2.5 rounded-full ${quota?.connected ? 'bg-emerald-500' : 'bg-slate-300'}`} /></CardHeader><CardContent className="space-y-5">{quota?.connected ? <><QuotaRow title="五小时窗口" value={quota.five_hour_remaining_pct} detail={`${new Date(quota.five_hour_reset_at).toLocaleString('zh-CN', { weekday: 'short', hour: '2-digit', minute: '2-digit' })} 刷新`} color={accent} /><QuotaRow title="周额度" value={quota.weekly_remaining_pct} detail="保留线以下不会调度" color={accent} /></> : <div className="rounded-xl border border-dashed p-5 text-center"><p className="text-sm font-medium">尚未读取到额度</p><p className="mt-1 break-words text-sm text-slate-500">{quota?.error || '只登录另一方也能使用'}</p></div>}</CardContent></Card>;
}
function QuotaRow({ title, value, detail, color }: { title: string; value: number; detail: string; color: string }) { return <div><div className="mb-2 flex items-end justify-between"><div><p className="text-sm font-medium">{title}</p><p className="text-xs text-slate-400">{detail}</p></div><span className="font-mono text-lg font-semibold" style={{ color }}>{Math.round(value)}%</span></div><Progress value={value} className="[&_[data-slot=progress-indicator]]:bg-[var(--c)]" style={{ '--c': color } as React.CSSProperties} /></div>; }
function Metric({ value, label }: { value: number; label: string }) { return <div className="rounded-xl bg-white/7 p-3"><p className="text-2xl font-semibold">{value}</p><p className="text-xs text-slate-400">{label}</p></div>; }
function CheckRow({ text, pass }: { text: string; pass: boolean }) { return <div className="flex justify-between text-sm"><span className="text-slate-600">{text}</span><span className={pass ? 'text-emerald-600' : 'text-slate-400'}>{pass ? '✓ 通过' : '◷ 等待'}</span></div>; }
function Empty({ text }: { text: string }) { return <p className="py-10 text-center text-sm text-slate-500">{text}</p>; }
function TaskSummary({ task, label }: { task: Task; label?: TaskLabel }) { return <div className="rounded-2xl border p-5"><div className="flex justify-between gap-4"><div><LabelPill label={label} /><h2 className="mt-4 text-xl font-semibold">{task.title}</h2><p className="mt-2 text-sm text-slate-500">{task.project_path || '尚未指定项目目录'}</p></div><ProviderBadge provider={task.provider_preference} /></div><div className="mt-5 flex gap-5 border-t pt-4 text-sm text-slate-600"><span>优先级 {task.priority}</span><span>{statusName[task.status]}</span></div></div>; }

function Queue({ tasks, labels, onOpen, refresh }: { tasks: Task[]; labels: TaskLabel[]; onOpen: (task: Task) => void; refresh: () => Promise<void> }) {
  async function toggle(task: Task) { await request(`/tasks/${task.id}`, { method: 'PATCH', body: JSON.stringify({ status: task.status === 'paused' ? 'queued' : 'paused' }) }); await refresh(); }
  return <Card className="border-0 bg-white ring-1 ring-slate-200"><CardHeader><CardTitle>任务与会话</CardTitle><p className="text-sm text-slate-500">回复会排队到下一次收割窗口，不会自动删除会话。</p></CardHeader><CardContent className="space-y-3">{tasks.length === 0 ? <Empty text="还没有任务" /> : tasks.map((task, index) => <div key={task.id} className="flex flex-col gap-4 rounded-2xl border p-4 sm:flex-row sm:items-center"><div className="flex size-9 items-center justify-center rounded-xl bg-slate-100 font-mono text-sm">{index + 1}</div><div className="min-w-0 flex-1"><div className="flex flex-wrap items-center gap-2"><h3 className="font-semibold">{task.title}</h3><LabelPill label={labels.find((l) => l.id === task.label_id)} /><ProviderBadge provider={task.provider_used || task.provider_preference} /></div><p className="mt-2 truncate text-sm text-slate-500">{task.last_error || task.project_path || statusName[task.status]}</p></div><div className="flex flex-wrap items-center gap-2"><Badge variant="outline">{statusName[task.status]}</Badge><Button variant="outline" size="sm" onClick={() => onOpen(task)}><MessageSquare />打开会话</Button><Button variant="ghost" size="sm" onClick={() => void toggle(task)} disabled={task.status === 'running'}>{task.status === 'paused' ? <Play /> : <Pause />}{task.status === 'paused' ? '启用' : '暂停'}</Button></div></div>)}</CardContent></Card>;
}

function Conversation({ task, open, close, refresh }: { task?: Task; open: boolean; close: () => void; refresh: () => Promise<void> }) {
  const [messages, setMessages] = useState<Message[]>([]); const [reply, setReply] = useState(''); const [busy, setBusy] = useState(false);
  const load = useCallback(async () => { if (!task) return; const result = await request<{ messages: Message[] }>(`/tasks/${task.id}/messages`); setMessages(result.messages ?? []); }, [task]);
  useEffect(() => { if (open) void load(); }, [open, load]);
  async function send() { if (!task || !reply.trim()) return; setBusy(true); try { await request(`/tasks/${task.id}/replies`, { method: 'POST', body: JSON.stringify({ content: reply }) }); setReply(''); await load(); await refresh(); } finally { setBusy(false); } }
  async function runNow() { if (!task || !window.confirm('立即运行会消耗当前额度，确定继续吗？')) return; await request(`/tasks/${task.id}/run`, { method: 'POST' }); await refresh(); }
  return <Dialog open={open} onOpenChange={(value) => !value && close()}><DialogContent className="flex max-h-[82vh] flex-col sm:max-w-2xl"><DialogHeader><DialogTitle>{task?.title}</DialogTitle><DialogDescription>{task ? statusName[task.status] : ''} · 回复默认等待下一收割窗口</DialogDescription></DialogHeader><div className="min-h-48 flex-1 space-y-3 overflow-y-auto rounded-xl bg-slate-50 p-4">{messages.length === 0 ? <Empty text="暂无消息" /> : messages.map((message) => <div key={message.id} className={`max-w-[88%] rounded-2xl px-4 py-3 text-sm leading-6 ${message.role === 'user' ? 'ml-auto bg-slate-950 text-white' : 'bg-white text-slate-700 ring-1 ring-slate-200'}`}><p className="whitespace-pre-wrap">{message.content}</p>{message.role === 'user' && <p className="mt-2 text-xs text-slate-400">{message.dispatched_at ? '已发送' : '等待额度窗口'}</p>}</div>)}</div><Textarea value={reply} onChange={(e) => setReply(e.target.value)} placeholder="回复这个任务；保存后不会立即运行。" /><DialogFooter className="sm:justify-between"><Button variant="outline" onClick={() => void runNow()} disabled={!task || task.status === 'running'}><Zap />立即运行</Button><Button onClick={() => void send()} disabled={busy || !reply.trim()}>{busy ? <Loader2 className="animate-spin" /> : <Clock3 />}排队到收割窗口</Button></DialogFooter></DialogContent></Dialog>;
}

function Labels({ labels, selected, select, refresh }: { labels: TaskLabel[]; selected: TaskLabel; select: (id: string) => void; refresh: () => Promise<void> }) {
  const [draft, setDraft] = useState(selected); useEffect(() => setDraft(selected), [selected]); async function save() { await request('/labels', { method: 'POST', body: JSON.stringify(draft) }); await refresh(); }
  return <div className="grid gap-5 lg:grid-cols-[340px_1fr]"><Card className="border-0 bg-white ring-1 ring-slate-200"><CardHeader><CardTitle>标签</CardTitle></CardHeader><CardContent className="space-y-2">{labels.map((label) => <button key={label.id} onClick={() => select(label.id)} className={`flex w-full items-center justify-between rounded-xl p-3 text-left ${selected.id === label.id ? 'bg-slate-100' : 'hover:bg-slate-50'}`}><span>{label.emoji} {label.name}</span><ChevronRight className="size-4" /></button>)}</CardContent></Card><Card className="border-0 bg-white ring-1 ring-slate-200"><CardHeader><CardTitle>编辑“{draft.name}”</CardTitle></CardHeader><CardContent className="grid gap-5 sm:grid-cols-2"><Field label="名称"><Input value={draft.name} onChange={(e) => setDraft({ ...draft, name: e.target.value })} /></Field><Field label="图标"><Input value={draft.emoji} onChange={(e) => setDraft({ ...draft, emoji: e.target.value })} /></Field><Field label="Codex 侧边栏分组"><Input value={draft.codex_section} onChange={(e) => setDraft({ ...draft, codex_section: e.target.value })} /></Field><Field label="颜色"><Input type="color" value={draft.color} onChange={(e) => setDraft({ ...draft, color: e.target.value })} /></Field><Field label="默认执行方"><ProviderSelect value={draft.default_provider} onChange={(value) => setDraft({ ...draft, default_provider: value })} /></Field><Field label="五小时最低剩余 %"><Input type="number" value={draft.min_window_remaining_pct} onChange={(e) => setDraft({ ...draft, min_window_remaining_pct: Number(e.target.value) })} /></Field><Field label="周额度保留 %"><Input type="number" value={draft.weekly_reserve_pct} onChange={(e) => setDraft({ ...draft, weekly_reserve_pct: Number(e.target.value) })} /></Field><div className="flex items-end"><Button className="w-full" onClick={() => void save()}>保存标签设置</Button></div></CardContent></Card></div>;
}

function HistoryView({ tasks }: { tasks: Task[] }) { const rows = tasks.filter((task) => task.provider_used || task.status === 'failed'); return <Card className="border-0 bg-white ring-1 ring-slate-200"><CardHeader><CardTitle>运行记录</CardTitle></CardHeader><CardContent className="space-y-3">{rows.length === 0 ? <Empty text="还没有运行记录" /> : rows.map((task) => <div key={task.id} className="flex justify-between rounded-xl border p-4"><div><p className="font-medium">{task.title}</p><p className="text-sm text-slate-500">{new Date(task.updated_at).toLocaleString('zh-CN')}</p></div><div className="flex gap-2"><ProviderBadge provider={task.provider_used} /><Badge variant="outline">{statusName[task.status]}</Badge></div></div>)}</CardContent></Card>; }
function Settings({ scheduler, setScheduler, quotas }: { scheduler: boolean; setScheduler: (value: boolean) => Promise<void>; quotas: Quota[] }) { return <div className="grid gap-5 lg:grid-cols-2"><Card className="border-0 bg-white ring-1 ring-slate-200"><CardHeader><CardTitle>执行安全</CardTitle></CardHeader><CardContent className="space-y-5"><Setting title="启用自动执行" detail="默认关闭；开启后才会调用模型"><Switch checked={scheduler} onCheckedChange={(value) => void setScheduler(value)} /></Setting><Setting title="刷新前安全停止" detail="默认提前 8 分钟停止"><Check className="text-emerald-600" /></Setting><Setting title="安全权限模式" detail="不绕过沙箱和批准"><Check className="text-emerald-600" /></Setting></CardContent></Card><Card className="border-0 bg-white ring-1 ring-slate-200"><CardHeader><CardTitle>提供方连接</CardTitle></CardHeader><CardContent className="space-y-3">{(['codex', 'claude'] as const).map((name) => { const quota = quotas.find((q) => q.provider === name); return <div key={name} className="rounded-xl border p-4"><p className="font-medium capitalize">{name}</p><p className="mt-1 text-sm text-slate-500">{quota?.connected ? '额度读取正常' : quota?.error || '尚未连接'}</p></div>; })}</CardContent></Card></div>; }
function Setting({ title, detail, children }: { title: string; detail: string; children: React.ReactNode }) { return <div className="flex items-center justify-between gap-4"><div><p className="font-medium">{title}</p><p className="text-sm text-slate-500">{detail}</p></div>{children}</div>; }
function Field({ label, children }: { label: string; children: React.ReactNode }) { return <div className="space-y-2"><FieldLabel>{label}</FieldLabel>{children}</div>; }

function AddTask({ open, onOpenChange, labels, online, onCreated }: { open: boolean; onOpenChange: (value: boolean) => void; labels: TaskLabel[]; online: boolean; onCreated: () => Promise<void> }) {
  const [title, setTitle] = useState(''); const [project, setProject] = useState(''); const [prompt, setPrompt] = useState(''); const [provider, setProvider] = useState<Provider>('auto'); const [labelId, setLabelId] = useState('side'); const [busy, setBusy] = useState(false);
  async function create() { if (!title.trim() || !project.trim() || !prompt.trim()) return; setBusy(true); try { await request('/tasks', { method: 'POST', body: JSON.stringify({ title, prompt, project_path: project, provider_preference: provider, label_id: labelId, priority: 50 }) }); setTitle(''); setProject(''); setPrompt(''); onOpenChange(false); await onCreated(); } finally { setBusy(false); } }
  return <Dialog open={open} onOpenChange={onOpenChange}><DialogTrigger render={<Button size="sm" disabled={!online} />}><Plus />添加任务</DialogTrigger><DialogContent className="sm:max-w-lg"><DialogHeader><DialogTitle>添加持久任务</DialogTitle><DialogDescription>只保存并排队，不会立即调用模型。</DialogDescription></DialogHeader><div className="space-y-4"><Field label="任务名称"><Input value={title} onChange={(e) => setTitle(e.target.value)} /></Field><Field label="项目目录"><Input value={project} onChange={(e) => setProject(e.target.value)} placeholder="/Users/you/code/project" /></Field><Field label="初始 Prompt"><Textarea value={prompt} onChange={(e) => setPrompt(e.target.value)} /></Field><div className="grid gap-4 sm:grid-cols-2"><Field label="标签"><Select value={labelId} onValueChange={(value) => setLabelId(String(value))}><SelectTrigger className="w-full"><SelectValue /></SelectTrigger><SelectContent>{labels.map((label) => <SelectItem key={label.id} value={label.id}>{label.emoji} {label.name}</SelectItem>)}</SelectContent></Select></Field><Field label="执行方"><ProviderSelect value={provider} onChange={setProvider} /></Field></div></div><DialogFooter><Button variant="outline" onClick={() => onOpenChange(false)}>取消</Button><Button onClick={() => void create()} disabled={busy || !title.trim() || !project.trim() || !prompt.trim()}>{busy && <Loader2 className="animate-spin" />}加入队列</Button></DialogFooter></DialogContent></Dialog>;
}
