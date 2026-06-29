import { api } from './client'
import type { AppDefinition, AppDefinitionList } from '../types/appdefinition'

export const appdefinitions = {
  list: () => api.get<AppDefinitionList>('/api/v1/appdefinitions'),

  listInNamespace: (namespace: string) =>
    api.get<AppDefinitionList>(`/api/v1/namespaces/${namespace}/appdefinitions`),

  get: (namespace: string, name: string) =>
    api.get<AppDefinition>(`/api/v1/namespaces/${namespace}/appdefinitions/${name}`),

  create: (namespace: string, app: AppDefinition) =>
    api.post<AppDefinition>(`/api/v1/namespaces/${namespace}/appdefinitions`, app),

  update: (namespace: string, name: string, app: AppDefinition) =>
    api.put<AppDefinition>(`/api/v1/namespaces/${namespace}/appdefinitions/${name}`, app),

  delete: (namespace: string, name: string) =>
    api.delete(`/api/v1/namespaces/${namespace}/appdefinitions/${name}`),
}
