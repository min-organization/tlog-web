import type { QueryParams, SessionListResp } from '../types'

const TOKEN_KEY = 'tlog_token'

export function getToken(): string {
  return localStorage.getItem(TOKEN_KEY) || ''
}

export function setToken(t: string) {
  localStorage.setItem(TOKEN_KEY, t)
}

export function getUsername(): string {
  return localStorage.getItem('tlog_user') || ''
}

export function setUsername(u: string) {
  localStorage.setItem('tlog_user', u)
}

export function clearToken() {
  localStorage.removeItem(TOKEN_KEY)
  localStorage.removeItem('tlog_user')
}

function authHeader(): Record<string, string> {
  const t = getToken()
  return t ? { Authorization: `Bearer ${t}` } : {}
}

async function getJSON<T>(url: string): Promise<T> {
  const resp = await fetch(url, { credentials: 'same-origin', headers: authHeader() })
  if (resp.status === 401) {
    clearToken()
    throw new Error('unauthorized')
  }
  if (!resp.ok) {
    throw new Error(`请求失败 ${resp.status}: ${url}`)
  }
  return (await resp.json()) as T
}

export async function login(user: string, key: string): Promise<string> {
  const resp = await fetch('/api/login', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ user, key }),
  })
  if (resp.status === 401) {
    throw new Error('用户名或密码错误')
  }
  if (!resp.ok) {
    throw new Error(`登录失败 ${resp.status}`)
  }
  const data = (await resp.json()) as { token: string }
  setToken(data.token)
  setUsername(user)
  return data.token
}

export async function fetchSessions(params: QueryParams): Promise<SessionListResp> {
  const qs = new URLSearchParams()
  qs.set('page', String(params.page))
  qs.set('page_size', String(params.page_size))
  if (params.user) qs.set('user', params.user)
  if (params.q) qs.set('q', params.q)
  if (params.date_from) qs.set('date_from', params.date_from)
  if (params.date_to) qs.set('date_to', params.date_to)
  return getJSON<SessionListResp>(`/api/sessions?${qs.toString()}`)
}

export async function fetchUsers(): Promise<string[]> {
  return getJSON<string[]>('/api/users')
}

// 登出：吊销后端 token（使当前 token 立即失效），再清前端存储
export async function logout(): Promise<void> {
  try {
    await fetch('/api/logout', { method: 'POST', headers: authHeader() })
  } catch {
    // 忽略网络错误，前端清理照常进行
  }
  clearToken()
}

// 回放 WebSocket：token 经 Sec-WebSocket-Protocol 子协议传递（不在 URL，避免泄露到日志/历史）。
// 注意：RFC6455 子协议名只能是 token 字符集（不含 '.'），JWT 含 '.' 非法，
// 故对 token 做 base64url 编码（字符集 A-Za-z0-9-_ 全合法）后作为子协议第二项。
function b64urlEncode(s: string): string {
  const b64 = btoa(unescape(encodeURIComponent(s)))
  return b64.replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
}

export function openReplayWS(rec: string, speed: number): WebSocket {
  const proto = location.protocol === 'https:' ? 'wss:' : 'ws:'
  const url = `${proto}//${location.host}/api/ws/play/${encodeURIComponent(rec)}?speed=${speed}`
  const token = getToken()
  const ws = new WebSocket(url, ['Bearer', b64urlEncode(token)])
  ws.binaryType = 'arraybuffer' // 关键：二进制帧按 ArrayBuffer 解析
  return ws
}
