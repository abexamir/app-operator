const BASE_URL = import.meta.env.VITE_API_URL ?? ''
const TOKEN_KEY = 'app-operator-access-token'
const NAMESPACE_KEY = 'app-operator-active-namespace'

export function getAccessToken() {
  return sessionStorage.getItem(TOKEN_KEY) ?? ''
}

export function setAccessToken(token: string) {
  const trimmed = token.trim()
  if (trimmed) sessionStorage.setItem(TOKEN_KEY, trimmed)
  else sessionStorage.removeItem(TOKEN_KEY)
}

export function getActiveNamespace() {
  return sessionStorage.getItem(NAMESPACE_KEY) ?? 'default'
}

export function setActiveNamespace(namespace: string) {
  sessionStorage.setItem(NAMESPACE_KEY, namespace.trim() || 'default')
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
	const token = getAccessToken()
  const res = await fetch(`${BASE_URL}${path}`, {
		headers: {
			'Content-Type': 'application/json',
			...(token ? { Authorization: `Bearer ${token}` } : {}),
			...init?.headers,
		},
    ...init,
  })
  if (!res.ok) {
    const body = await res.json().catch(() => ({ error: res.statusText }))
    throw new Error(body.error ?? res.statusText)
  }
  if (res.status === 204) return undefined as T
  return res.json()
}

export const api = {
  get: <T>(path: string) => request<T>(path),
  post: <T>(path: string, body: unknown) =>
    request<T>(path, { method: 'POST', body: JSON.stringify(body) }),
  put: <T>(path: string, body: unknown) =>
    request<T>(path, { method: 'PUT', body: JSON.stringify(body) }),
  delete: (path: string) => request<void>(path, { method: 'DELETE' }),
}
