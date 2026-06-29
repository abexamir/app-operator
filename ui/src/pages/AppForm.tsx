import { useEffect, useState } from 'react'
import { useForm, useFieldArray, Controller, type Control, type UseFormRegister } from 'react-hook-form'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useNavigate, useParams, Link } from 'react-router-dom'
import Box from '@mui/material/Box'
import Paper from '@mui/material/Paper'
import Typography from '@mui/material/Typography'
import Button from '@mui/material/Button'
import IconButton from '@mui/material/IconButton'
import Tabs from '@mui/material/Tabs'
import Tab from '@mui/material/Tab'
import TextField from '@mui/material/TextField'
import MenuItem from '@mui/material/MenuItem'
import Switch from '@mui/material/Switch'
import FormControlLabel from '@mui/material/FormControlLabel'
import Divider from '@mui/material/Divider'
import Alert from '@mui/material/Alert'
import Breadcrumbs from '@mui/material/Breadcrumbs'
import Accordion from '@mui/material/Accordion'
import AccordionSummary from '@mui/material/AccordionSummary'
import AccordionDetails from '@mui/material/AccordionDetails'
import CircularProgress from '@mui/material/CircularProgress'
import Chip from '@mui/material/Chip'
import NavigateNextIcon from '@mui/icons-material/NavigateNext'
import ExpandMoreIcon from '@mui/icons-material/ExpandMore'
import AddIcon from '@mui/icons-material/Add'
import DeleteOutlinedIcon from '@mui/icons-material/DeleteOutlined'
import { appdefinitions } from '../api/appdefinitions'
import type { AppDefinition } from '../types/appdefinition'

// ── helpers ──────────────────────────────────────────────────────────────────

function Row({ children }: { children: React.ReactNode }) {
  return <Box sx={{ display: 'flex', gap: 2, mb: 2, flexWrap: 'wrap' }}>{children}</Box>
}
function SectionTitle({ children }: { children: React.ReactNode }) {
  return <Typography variant="subtitle2" color="text.secondary" sx={{ mt: 2, mb: 1.5 }}>{children}</Typography>
}
function AddBtn({ label, onClick }: { label: string; onClick: () => void }) {
  return (
    <Button size="small" startIcon={<AddIcon />} onClick={onClick} variant="outlined" sx={{ mt: 1 }}>
      {label}
    </Button>
  )
}
function RemoveBtn({ onClick }: { onClick: () => void }) {
  return (
    <IconButton size="small" color="error" onClick={onClick} sx={{ mt: 0.5 }}>
      <DeleteOutlinedIcon fontSize="small" />
    </IconButton>
  )
}

function JsonField({ label, value, onChange }: { label: string; value: string; onChange: (v: string) => void }) {
  const [err, setErr] = useState('')
  return (
    <Box sx={{ mb: 2 }}>
      <TextField
        label={label}
        multiline
        minRows={4}
        maxRows={12}
        fullWidth
        value={value}
        onChange={e => {
          onChange(e.target.value)
          try { JSON.parse(e.target.value); setErr('') } catch { setErr('Invalid JSON') }
        }}
        error={!!err}
        helperText={err || 'JSON'}
        slotProps={{ htmlInput: { style: { fontFamily: 'monospace', fontSize: '0.78rem' } } }}
      />
    </Box>
  )
}

// ── form shape ────────────────────────────────────────────────────────────────

interface PortForm { name: string; containerPort: number; servicePort: number; protocol: string; expose: boolean; metricsEnabled: boolean; metricsPath: string; metricsInterval: string }
interface EnvForm { name: string; value: string }
interface ContainerForm { name: string; image: string; command: string; args: string; env: EnvForm[]; ports: PortForm[]; resourcesJson: string; readinessProbeJson: string; livenessProbeJson: string }
interface DomainForm { name: string; tls: boolean; redirectTls: boolean; certIssuer: string; path: string; portName: string; secretName: string; annotationsJson: string }
interface DiskPartitionForm { subPath: string; mountPath: string }
interface ConfigMapForm { name: string; mountPath: string; dataJson: string }
interface SecretForm { name: string; mountPath: string; asEnvVars: boolean; secretRef: string; dataJson: string }
interface ExternalSecretDataRow { secretKey: string; remoteKey: string; remoteProperty: string }
interface ExternalSecretForm { name: string; store: string; storeKind: string; refreshInterval: string; mountPath: string; asEnvVars: boolean; data: ExternalSecretDataRow[]; dataFromJson: string }

interface FormValues {
  name: string
  namespace: string
  replicas: number
  serviceType: string
  ingressClass: string
  ingressAnnotationsJson: string
  paused: boolean
  imagePullSecrets: string          // comma-separated
  nodeSelectorsJson: string
  tolerationsJson: string
  affinityJson: string
  securityContextJson: string
  lifecycleJson: string
  loggingConfigJson: string
  containers: ContainerForm[]
  initContainers: ContainerForm[]
  domains: DomainForm[]
  diskEnabled: boolean
  diskSizeInGi: number
  diskStorageClassName: string
  diskSetFsGroup: boolean
  diskProtect: boolean
  diskAnnotationsJson: string
  diskPartitions: DiskPartitionForm[]
  autoscalingEnabled: boolean
  autoscalingMin: number
  autoscalingMax: number
  autoscalingCpu: number
  autoscalingMemory: number
  configMaps: ConfigMapForm[]
  secrets: SecretForm[]
  externalSecrets: ExternalSecretForm[]
}

const defaultContainer: ContainerForm = { name: '', image: '', command: '', args: '', env: [], ports: [], resourcesJson: '', readinessProbeJson: '', livenessProbeJson: '' }
const defaultPort: PortForm = { name: 'http', containerPort: 8080, servicePort: 80, protocol: 'TCP', expose: true, metricsEnabled: false, metricsPath: '/metrics', metricsInterval: '' }
const defaultDomain: DomainForm = { name: '', tls: true, redirectTls: true, certIssuer: '', path: '/', portName: 'http', secretName: '', annotationsJson: '' }

const defaults: FormValues = {
  name: '', namespace: 'default', replicas: 1, serviceType: 'ClusterIP',
  ingressClass: '', ingressAnnotationsJson: '', paused: false, imagePullSecrets: '',
  nodeSelectorsJson: '', tolerationsJson: '', affinityJson: '', securityContextJson: '',
  lifecycleJson: '', loggingConfigJson: '',
  containers: [{ ...defaultContainer, name: 'app', ports: [{ ...defaultPort }] }],
  initContainers: [],
  domains: [],
  diskEnabled: false, diskSizeInGi: 10, diskStorageClassName: '', diskSetFsGroup: false,
  diskProtect: false, diskAnnotationsJson: '', diskPartitions: [],
  autoscalingEnabled: false, autoscalingMin: 1, autoscalingMax: 5, autoscalingCpu: 70, autoscalingMemory: 0,
  configMaps: [], secrets: [], externalSecrets: [],
}

// ── spec builder ──────────────────────────────────────────────────────────────

function parseJsonOr<T>(s: string, fallback: T): T {
  if (!s.trim()) return fallback
  try { return JSON.parse(s) } catch { return fallback }
}

function buildContainer(c: ContainerForm) {
  return {
    name: c.name,
    image: c.image,
    ...(c.command.trim() ? { command: c.command.trim().split(/\s+/) } : {}),
    ...(c.args.trim() ? { args: c.args.trim().split(/\s+/) } : {}),
    env: c.env.filter(e => e.name),
    ports: c.ports.map(p => ({
      name: p.name, containerPort: +p.containerPort, servicePort: +p.servicePort,
      protocol: p.protocol, expose: p.expose,
      ...(p.metricsEnabled ? { metrics: { enabled: true, path: p.metricsPath, ...(p.metricsInterval ? { interval: p.metricsInterval } : {}) } } : {}),
    })),
    ...parseJsonOr<{ resources?: object }>(c.resourcesJson, {}),
    ...(c.readinessProbeJson.trim() ? { readinessProbe: parseJsonOr(c.readinessProbeJson, undefined) } : {}),
    ...(c.livenessProbeJson.trim() ? { livenessProbe: parseJsonOr(c.livenessProbeJson, undefined) } : {}),
  }
}

function buildSpec(v: FormValues) {
  return {
    replicas: +v.replicas,
    serviceType: v.serviceType || undefined,
    ingressClass: v.ingressClass || undefined,
    ingressAnnotations: parseJsonOr<Record<string, string> | undefined>(v.ingressAnnotationsJson, undefined),
    paused: v.paused || undefined,
    imagePullSecrets: v.imagePullSecrets.split(',').map(s => s.trim()).filter(Boolean).map(n => ({ name: n })),
    nodeSelector: parseJsonOr<Record<string, string> | undefined>(v.nodeSelectorsJson, undefined),
    tolerations: parseJsonOr<unknown[] | undefined>(v.tolerationsJson, undefined),
    affinity: parseJsonOr(v.affinityJson, undefined),
    securityContext: parseJsonOr(v.securityContextJson, undefined),
    lifecycle: parseJsonOr(v.lifecycleJson, undefined),
    loggingConfig: parseJsonOr(v.loggingConfigJson, undefined),
    containers: v.containers.map(buildContainer),
    initContainers: v.initContainers.length > 0 ? v.initContainers.map(buildContainer) : undefined,
    domains: v.domains.length > 0 ? v.domains.map(d => ({
      name: d.name, tls: d.tls, redirect_tls: d.redirectTls || undefined,
      certIssuer: d.certIssuer || undefined, path: d.path || undefined,
      portName: d.portName || undefined, secretName: d.secretName || undefined,
      annotations: parseJsonOr<Record<string, string> | undefined>(d.annotationsJson, undefined),
    })) : undefined,
    disk: v.diskEnabled ? {
      sizeInGi: +v.diskSizeInGi, storageClassName: v.diskStorageClassName || undefined,
      setFsGroup: v.diskSetFsGroup || undefined, protect: v.diskProtect || undefined,
      annotations: parseJsonOr(v.diskAnnotationsJson, undefined),
      partitions: v.diskPartitions.filter(p => p.subPath),
    } : undefined,
    autoscaling: v.autoscalingEnabled ? {
      enabled: true, minReplicas: +v.autoscalingMin, maxReplicas: +v.autoscalingMax,
      ...(v.autoscalingCpu > 0 ? { targetCPUUtilizationPercentage: +v.autoscalingCpu } : {}),
      ...(v.autoscalingMemory > 0 ? { targetMemoryUtilizationPercentage: +v.autoscalingMemory } : {}),
    } : undefined,
    configMaps: v.configMaps.map(cm => ({ name: cm.name, mountPath: cm.mountPath, data: parseJsonOr<Record<string, string>>(cm.dataJson, {}) })),
    secrets: v.secrets.map(s => ({
      name: s.name, mountPath: s.mountPath || undefined, asEnvVars: s.asEnvVars || undefined,
      secretRef: s.secretRef || undefined,
      data: s.dataJson ? parseJsonOr<Record<string, string> | undefined>(s.dataJson, undefined) : undefined,
    })),
    externalSecrets: v.externalSecrets.map(es => ({
      name: es.name, store: es.store, storeKind: es.storeKind || undefined,
      refreshInterval: es.refreshInterval || undefined, mountPath: es.mountPath || undefined,
      asEnvVars: es.asEnvVars || undefined,
      data: es.data.filter(d => d.secretKey).map(d => ({ secretKey: d.secretKey, remoteRef: { key: d.remoteKey, property: d.remoteProperty || undefined } })),
      dataFrom: parseJsonOr<unknown[] | undefined>(es.dataFromJson, undefined),
    })),
  }
}

// ── spec → form ───────────────────────────────────────────────────────────────

function appToForm(app: AppDefinition): FormValues {
  const s = app.spec
  const toContainerForm = (c: ReturnType<typeof buildContainer>): ContainerForm => ({
    name: (c as { name: string }).name,
    image: (c as { image: string }).image,
    command: ((c as { command?: string[] }).command ?? []).join(' '),
    args: ((c as { args?: string[] }).args ?? []).join(' '),
    env: ((c as { env?: { name: string; value?: string }[] }).env ?? []).map(e => ({ name: e.name, value: e.value ?? '' })),
    ports: ((c as { ports?: { name: string; containerPort: number; servicePort: number; protocol?: string; expose?: boolean; metrics?: { enabled: boolean; path?: string; interval?: string } }[] }).ports ?? []).map(p => ({
      name: p.name, containerPort: p.containerPort, servicePort: p.servicePort,
      protocol: p.protocol ?? 'TCP', expose: p.expose ?? true,
      metricsEnabled: p.metrics?.enabled ?? false,
      metricsPath: p.metrics?.path ?? '/metrics',
      metricsInterval: p.metrics?.interval ?? '',
    })),
    resourcesJson: (c as { resources?: unknown }).resources ? JSON.stringify((c as { resources: unknown }).resources, null, 2) : '',
    readinessProbeJson: (c as { readinessProbe?: unknown }).readinessProbe ? JSON.stringify((c as { readinessProbe: unknown }).readinessProbe, null, 2) : '',
    livenessProbeJson: (c as { livenessProbe?: unknown }).livenessProbe ? JSON.stringify((c as { livenessProbe: unknown }).livenessProbe, null, 2) : '',
  })
  return {
    name: app.metadata.name,
    namespace: app.metadata.namespace,
    replicas: s.replicas ?? 1,
    serviceType: s.serviceType ?? 'ClusterIP',
    ingressClass: s.ingressClass ?? '',
    ingressAnnotationsJson: s.ingressAnnotations ? JSON.stringify(s.ingressAnnotations, null, 2) : '',
    paused: s.paused ?? false,
    imagePullSecrets: (s.imagePullSecrets ?? []).map(p => p.name).join(', '),
    nodeSelectorsJson: s.nodeSelector ? JSON.stringify(s.nodeSelector, null, 2) : '',
    tolerationsJson: s.tolerations ? JSON.stringify(s.tolerations, null, 2) : '',
    affinityJson: s.affinity ? JSON.stringify(s.affinity, null, 2) : '',
    securityContextJson: s.securityContext ? JSON.stringify(s.securityContext, null, 2) : '',
    lifecycleJson: s.lifecycle ? JSON.stringify(s.lifecycle, null, 2) : '',
    loggingConfigJson: s.loggingConfig ? JSON.stringify(s.loggingConfig, null, 2) : '',
    containers: (s.containers ?? []).map(c => toContainerForm(c as unknown as ReturnType<typeof buildContainer>)),
    initContainers: (s.initContainers ?? []).map(c => toContainerForm(c as unknown as ReturnType<typeof buildContainer>)),
    domains: (s.domains ?? []).map(d => ({
      name: d.name, tls: d.tls, redirectTls: d.redirect_tls ?? false,
      certIssuer: d.certIssuer ?? '', path: d.path ?? '/', portName: d.portName ?? 'http',
      secretName: d.secretName ?? '', annotationsJson: d.annotations ? JSON.stringify(d.annotations, null, 2) : '',
    })),
    diskEnabled: !!s.disk,
    diskSizeInGi: s.disk?.sizeInGi ?? 10,
    diskStorageClassName: s.disk?.storageClassName ?? '',
    diskSetFsGroup: s.disk?.setFsGroup ?? false,
    diskProtect: s.disk?.protect ?? false,
    diskAnnotationsJson: s.disk?.annotations ? JSON.stringify(s.disk.annotations, null, 2) : '',
    diskPartitions: s.disk?.partitions ?? [],
    autoscalingEnabled: s.autoscaling?.enabled ?? false,
    autoscalingMin: s.autoscaling?.minReplicas ?? 1,
    autoscalingMax: s.autoscaling?.maxReplicas ?? 5,
    autoscalingCpu: s.autoscaling?.targetCPUUtilizationPercentage ?? 70,
    autoscalingMemory: s.autoscaling?.targetMemoryUtilizationPercentage ?? 0,
    configMaps: (s.configMaps ?? []).map(cm => ({ name: cm.name, mountPath: cm.mountPath, dataJson: JSON.stringify(cm.data, null, 2) })),
    secrets: (s.secrets ?? []).map(sec => ({ name: sec.name, mountPath: sec.mountPath ?? '', asEnvVars: sec.asEnvVars ?? false, secretRef: sec.secretRef ?? '', dataJson: sec.data ? JSON.stringify(sec.data, null, 2) : '' })),
    externalSecrets: (s.externalSecrets ?? []).map(es => ({
      name: es.name, store: es.store, storeKind: es.storeKind ?? 'ClusterSecretStore',
      refreshInterval: es.refreshInterval ?? '1h', mountPath: es.mountPath ?? '',
      asEnvVars: es.asEnvVars ?? false,
      data: (es.data ?? []).map(d => ({ secretKey: d.secretKey, remoteKey: d.remoteRef.key, remoteProperty: d.remoteRef.property ?? '' })),
      dataFromJson: es.dataFrom ? JSON.stringify(es.dataFrom, null, 2) : '',
    })),
  }
}

// ── container sub-form ────────────────────────────────────────────────────────

function ContainerFields({ prefix, control, register, remove }: {
  prefix: `containers.${number}` | `initContainers.${number}`
  control: Control<FormValues>
  register: UseFormRegister<FormValues>
  remove?: () => void
}) {
  const { fields: ports, append: addPort, remove: removePort } = useFieldArray({ control, name: `${prefix}.ports` as `containers.${number}.ports` })
  const { fields: envs, append: addEnv, remove: removeEnv } = useFieldArray({ control, name: `${prefix}.env` as `containers.${number}.env` })

  return (
    <Box>
      <Row>
        <TextField size="small" label="Container name *" sx={{ flex: 1 }} {...register(`${prefix}.name` as `containers.${number}.name`, { required: true })} />
        <TextField size="small" label="Image *" sx={{ flex: 2 }} {...register(`${prefix}.image` as `containers.${number}.image`, { required: true })} />
        {remove && <RemoveBtn onClick={remove} />}
      </Row>
      <Row>
        <TextField size="small" label="Command (space-separated)" sx={{ flex: 1 }} {...register(`${prefix}.command` as `containers.${number}.command`)} />
        <TextField size="small" label="Args (space-separated)" sx={{ flex: 1 }} {...register(`${prefix}.args` as `containers.${number}.args`)} />
      </Row>

      <SectionTitle>Environment Variables</SectionTitle>
      {envs.map((env, ei) => (
        <Row key={env.id}>
          <TextField size="small" label="Name" sx={{ flex: 1 }} {...register(`${prefix}.env.${ei}.name` as `containers.${number}.env.${number}.name`)} />
          <TextField size="small" label="Value" sx={{ flex: 2 }} {...register(`${prefix}.env.${ei}.value` as `containers.${number}.env.${number}.value`)} />
          <RemoveBtn onClick={() => removeEnv(ei)} />
        </Row>
      ))}
      <AddBtn label="Add Env Var" onClick={() => addEnv({ name: '', value: '' })} />

      <SectionTitle>Ports</SectionTitle>
      {ports.map((port, pi) => (
        <Accordion key={port.id} defaultExpanded={pi === 0} sx={{ mb: 1 }}>
          <AccordionSummary expandIcon={<ExpandMoreIcon />}>
            <Typography variant="body2" sx={{ fontWeight: 500 }}>Port {pi + 1}</Typography>
          </AccordionSummary>
          <AccordionDetails>
            <Row>
              <TextField size="small" label="Name" sx={{ flex: 1 }} {...register(`${prefix}.ports.${pi}.name` as `containers.${number}.ports.${number}.name`)} />
              <TextField size="small" label="Container Port" type="number" sx={{ flex: 1 }} {...register(`${prefix}.ports.${pi}.containerPort` as `containers.${number}.ports.${number}.containerPort`)} />
              <TextField size="small" label="Service Port" type="number" sx={{ flex: 1 }} {...register(`${prefix}.ports.${pi}.servicePort` as `containers.${number}.ports.${number}.servicePort`)} />
              <TextField size="small" label="Protocol" select sx={{ flex: 0.8 }} defaultValue="TCP" {...register(`${prefix}.ports.${pi}.protocol` as `containers.${number}.ports.${number}.protocol`)}>
                <MenuItem value="TCP">TCP</MenuItem>
                <MenuItem value="UDP">UDP</MenuItem>
              </TextField>
            </Row>
            <Row>
              <Controller control={control} name={`${prefix}.ports.${pi}.expose` as `containers.${number}.ports.${number}.expose`}
                render={({ field }) => <FormControlLabel control={<Switch checked={!!field.value} onChange={e => field.onChange(e.target.checked)} />} label="Expose via Service" />} />
              <Controller control={control} name={`${prefix}.ports.${pi}.metricsEnabled` as `containers.${number}.ports.${number}.metricsEnabled`}
                render={({ field }) => <FormControlLabel control={<Switch checked={!!field.value} onChange={e => field.onChange(e.target.checked)} />} label="Prometheus Metrics" />} />
            </Row>
            <Controller control={control} name={`${prefix}.ports.${pi}.metricsEnabled` as `containers.${number}.ports.${number}.metricsEnabled`}
              render={({ field: mf }) => (
                <Box sx={{ display: mf.value ? 'block' : 'none' }}>
                  <Row>
                    <TextField size="small" label="Metrics Path" sx={{ flex: 1 }} {...register(`${prefix}.ports.${pi}.metricsPath` as `containers.${number}.ports.${number}.metricsPath`)} />
                    <TextField size="small" label="Scrape Interval (e.g. 15s)" sx={{ flex: 1 }} {...register(`${prefix}.ports.${pi}.metricsInterval` as `containers.${number}.ports.${number}.metricsInterval`)} />
                  </Row>
                </Box>
              )} />
            <Box sx={{ display: 'flex', justifyContent: 'flex-end' }}>
              <Button size="small" color="error" startIcon={<DeleteOutlinedIcon />} onClick={() => removePort(pi)}>Remove Port</Button>
            </Box>
          </AccordionDetails>
        </Accordion>
      ))}
      <AddBtn label="Add Port" onClick={() => addPort({ ...defaultPort })} />

      <SectionTitle>Resources (JSON)</SectionTitle>
      <Controller control={control} name={`${prefix}.resourcesJson` as `containers.${number}.resourcesJson`}
        render={({ field }) => <JsonField label="Resources" value={field.value} onChange={field.onChange} />} />

      <SectionTitle>Probes (JSON)</SectionTitle>
      <Row>
        <Box sx={{ flex: 1 }}>
          <Controller control={control} name={`${prefix}.readinessProbeJson` as `containers.${number}.readinessProbeJson`}
            render={({ field }) => <JsonField label="Readiness Probe" value={field.value} onChange={field.onChange} />} />
        </Box>
        <Box sx={{ flex: 1 }}>
          <Controller control={control} name={`${prefix}.livenessProbeJson` as `containers.${number}.livenessProbeJson`}
            render={({ field }) => <JsonField label="Liveness Probe" value={field.value} onChange={field.onChange} />} />
        </Box>
      </Row>
    </Box>
  )
}

// ── main form ─────────────────────────────────────────────────────────────────

const TABS = ['General', 'Containers', 'Init Containers', 'Network', 'Storage', 'Config', 'Scaling', 'Advanced']

export function AppForm() {
  const { namespace, name } = useParams<{ namespace?: string; name?: string }>()
  const isEdit = !!(namespace && name)
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const [tab, setTab] = useState(0)

  const { register, control, handleSubmit, reset, watch, formState: { errors } } = useForm<FormValues>({ defaultValues: defaults })

  const { data: existing, isLoading } = useQuery({
    queryKey: ['appdefinitions', namespace, name],
    queryFn: () => appdefinitions.get(namespace!, name!),
    enabled: isEdit,
  })

  useEffect(() => {
    if (existing) reset(appToForm(existing))
  }, [existing, reset])

  const { fields: containers, append: addContainer, remove: removeContainer } = useFieldArray({ control, name: 'containers' })
  const { fields: initContainers, append: addInitContainer, remove: removeInitContainer } = useFieldArray({ control, name: 'initContainers' })
  const { fields: domains, append: addDomain, remove: removeDomain } = useFieldArray({ control, name: 'domains' })
  const { fields: diskPartitions, append: addPartition, remove: removePartition } = useFieldArray({ control, name: 'diskPartitions' })
  const { fields: configMaps, append: addConfigMap, remove: removeConfigMap } = useFieldArray({ control, name: 'configMaps' })
  const { fields: secrets, append: addSecret, remove: removeSecret } = useFieldArray({ control, name: 'secrets' })
  const { fields: externalSecrets, append: addExternalSecret, remove: removeExternalSecret } = useFieldArray({ control, name: 'externalSecrets' })

  const diskEnabled = watch('diskEnabled')
  const autoscalingEnabled = watch('autoscalingEnabled')

  const mutation = useMutation({
    mutationFn: (app: AppDefinition) =>
      isEdit
        ? appdefinitions.update(namespace!, name!, app)
        : appdefinitions.create(app.metadata.namespace, app),
    onSuccess: app => {
      queryClient.invalidateQueries({ queryKey: ['appdefinitions'] })
      navigate(`/namespaces/${app.metadata.namespace}/apps/${app.metadata.name}`)
    },
  })

  const onSubmit = (v: FormValues) => {
    mutation.mutate({
      apiVersion: 'appdefinition.abexamir.me/v1',
      kind: 'AppDefinition',
      metadata: { name: v.name, namespace: v.namespace },
      spec: buildSpec(v) as AppDefinition['spec'],
    })
  }

  if (isEdit && isLoading) return <Box sx={{ p: 4 }}><CircularProgress size={28} /></Box>

  return (
    <Box component="form" onSubmit={handleSubmit(onSubmit)} sx={{ p: 4 }}>
      <Breadcrumbs separator={<NavigateNextIcon fontSize="small" />} sx={{ mb: 2 }}>
        <Typography component={Link} to="/" variant="body2" sx={{ color: 'text.secondary', textDecoration: 'none', '&:hover': { color: 'text.primary' } }}>Applications</Typography>
        <Typography variant="body2" color="text.primary">{isEdit ? name : 'New Application'}</Typography>
      </Breadcrumbs>

      <Box sx={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', mb: 3 }}>
        <Typography variant="h5">{isEdit ? `Edit ${name}` : 'New Application'}</Typography>
        <Box sx={{ display: 'flex', gap: 1 }}>
          <Button variant="outlined" component={Link} to="/">Cancel</Button>
          <Button type="submit" variant="contained" disabled={mutation.isPending}>
            {mutation.isPending ? 'Saving…' : isEdit ? 'Save Changes' : 'Create App'}
          </Button>
        </Box>
      </Box>

      {mutation.error && <Alert severity="error" sx={{ mb: 2 }}>{String(mutation.error)}</Alert>}

      <Paper>
        <Tabs value={tab} onChange={(_, v) => setTab(v)} variant="scrollable" scrollButtons="auto"
          sx={{ borderBottom: 1, borderColor: 'divider', px: 2 }}>
          {TABS.map(t => <Tab key={t} label={t} />)}
        </Tabs>

        <Box sx={{ p: 3 }}>
          {/* ── General ── */}
          {tab === 0 && (
            <Box>
              <Row>
                <TextField size="small" label="App Name *" sx={{ flex: 1 }} disabled={isEdit} {...register('name', { required: true })} error={!!errors.name} />
                <TextField size="small" label="Namespace *" sx={{ flex: 1 }} disabled={isEdit} {...register('namespace', { required: true })} />
              </Row>
              <Row>
                <TextField size="small" label="Replicas" type="number" sx={{ flex: 0.5 }} {...register('replicas')} />
                <TextField size="small" label="Service Type" select sx={{ flex: 1 }} defaultValue="ClusterIP" {...register('serviceType')}>
                  {['ClusterIP', 'NodePort', 'LoadBalancer'].map(t => <MenuItem key={t} value={t}>{t}</MenuItem>)}
                </TextField>
              </Row>
              <Row>
                <TextField size="small" label="Image Pull Secrets (comma-separated)" sx={{ flex: 1 }} {...register('imagePullSecrets')} />
              </Row>
              <Controller control={control} name="paused"
                render={({ field }) => <FormControlLabel control={<Switch checked={field.value} onChange={e => field.onChange(e.target.checked)} />} label="Paused (suspend reconciliation)" sx={{ mb: 2 }} />} />
            </Box>
          )}

          {/* ── Containers ── */}
          {tab === 1 && (
            <Box>
              {containers.map((c, i) => (
                <Accordion key={c.id} defaultExpanded={i === 0} sx={{ mb: 2 }}>
                  <AccordionSummary expandIcon={<ExpandMoreIcon />}>
                    <Box sx={{ display: 'flex', alignItems: 'center', gap: 1 }}>
                      <Typography variant="body2" sx={{ fontWeight: 600 }}>Container {i + 1}</Typography>
                      <Chip size="small" label={watch(`containers.${i}.name`) || 'unnamed'} variant="outlined" />
                    </Box>
                  </AccordionSummary>
                  <AccordionDetails>
                    <ContainerFields prefix={`containers.${i}`} control={control} register={register} 
                      remove={containers.length > 1 ? () => removeContainer(i) : undefined} />
                  </AccordionDetails>
                </Accordion>
              ))}
              <AddBtn label="Add Container" onClick={() => addContainer({ ...defaultContainer })} />
            </Box>
          )}

          {/* ── Init Containers ── */}
          {tab === 2 && (
            <Box>
              {initContainers.length === 0 && (
                <Typography variant="body2" color="text.secondary" sx={{ mb: 2 }}>No init containers. Init containers run to completion before main containers start.</Typography>
              )}
              {initContainers.map((c, i) => (
                <Accordion key={c.id} defaultExpanded={i === 0} sx={{ mb: 2 }}>
                  <AccordionSummary expandIcon={<ExpandMoreIcon />}>
                    <Box sx={{ display: 'flex', alignItems: 'center', gap: 1 }}>
                      <Typography variant="body2" sx={{ fontWeight: 600 }}>Init Container {i + 1}</Typography>
                      <Chip size="small" label={watch(`initContainers.${i}.name`) || 'unnamed'} variant="outlined" />
                    </Box>
                  </AccordionSummary>
                  <AccordionDetails>
                    <ContainerFields prefix={`initContainers.${i}`} control={control} register={register} remove={() => removeInitContainer(i)} />
                  </AccordionDetails>
                </Accordion>
              ))}
              <AddBtn label="Add Init Container" onClick={() => addInitContainer({ ...defaultContainer })} />
            </Box>
          )}

          {/* ── Network ── */}
          {tab === 3 && (
            <Box>
              <SectionTitle>Ingress / Domains</SectionTitle>
              {domains.map((d, i) => (
                <Accordion key={d.id} defaultExpanded sx={{ mb: 1 }}>
                  <AccordionSummary expandIcon={<ExpandMoreIcon />}>
                    <Typography variant="body2" sx={{ fontWeight: 500 }}>{watch(`domains.${i}.name`) || `Domain ${i + 1}`}</Typography>
                  </AccordionSummary>
                  <AccordionDetails>
                    <Row>
                      <TextField size="small" label="Hostname *" sx={{ flex: 2 }} {...register(`domains.${i}.name`, { required: true })} />
                      <TextField size="small" label="Path" sx={{ flex: 0.8 }} {...register(`domains.${i}.path`)} />
                      <TextField size="small" label="Port Name" sx={{ flex: 0.8 }} {...register(`domains.${i}.portName`)} />
                    </Row>
                    <Row>
                      <Controller control={control} name={`domains.${i}.tls`}
                        render={({ field }) => <FormControlLabel control={<Switch checked={field.value} onChange={e => field.onChange(e.target.checked)} />} label="TLS" />} />
                      <Controller control={control} name={`domains.${i}.redirectTls`}
                        render={({ field }) => <FormControlLabel control={<Switch checked={field.value} onChange={e => field.onChange(e.target.checked)} />} label="Redirect HTTP→HTTPS" />} />
                    </Row>
                    <Row>
                      <TextField size="small" label="Cert Issuer" sx={{ flex: 1 }} {...register(`domains.${i}.certIssuer`)} />
                      <TextField size="small" label="TLS Secret Name" sx={{ flex: 1 }} {...register(`domains.${i}.secretName`)} />
                    </Row>
                    <Controller control={control} name={`domains.${i}.annotationsJson`}
                      render={({ field }) => <JsonField label="Domain Annotations" value={field.value} onChange={field.onChange} />} />
                    <Box sx={{ display: 'flex', justifyContent: 'flex-end' }}>
                      <Button size="small" color="error" startIcon={<DeleteOutlinedIcon />} onClick={() => removeDomain(i)}>Remove</Button>
                    </Box>
                  </AccordionDetails>
                </Accordion>
              ))}
              <AddBtn label="Add Domain" onClick={() => addDomain({ ...defaultDomain })} />

              <Divider sx={{ my: 3 }} />
              <SectionTitle>Ingress Settings</SectionTitle>
              <Row>
                <TextField size="small" label="Ingress Class" sx={{ flex: 1 }} {...register('ingressClass')} />
              </Row>
              <Controller control={control} name="ingressAnnotationsJson"
                render={({ field }) => <JsonField label="Ingress Annotations" value={field.value} onChange={field.onChange} />} />
            </Box>
          )}

          {/* ── Storage ── */}
          {tab === 4 && (
            <Box>
              <Controller control={control} name="diskEnabled"
                render={({ field }) => <FormControlLabel control={<Switch checked={field.value} onChange={e => field.onChange(e.target.checked)} />} label="Enable Persistent Disk" sx={{ mb: 2 }} />} />

              {diskEnabled && (
                <>
                  <Row>
                    <TextField size="small" label="Size (GiB) *" type="number" sx={{ flex: 0.5 }} {...register('diskSizeInGi', { min: 1 })} />
                    <TextField size="small" label="Storage Class" sx={{ flex: 1 }} placeholder="(cluster default)" {...register('diskStorageClassName')} />
                  </Row>
                  <Row>
                    <Controller control={control} name="diskSetFsGroup"
                      render={({ field }) => <FormControlLabel control={<Switch checked={field.value} onChange={e => field.onChange(e.target.checked)} />} label="Set fsGroup" />} />
                    <Controller control={control} name="diskProtect"
                      render={({ field }) => <FormControlLabel control={<Switch checked={field.value} onChange={e => field.onChange(e.target.checked)} />} label="Protect (block auto-create)" />} />
                  </Row>
                  <Controller control={control} name="diskAnnotationsJson"
                    render={({ field }) => <JsonField label="PVC Annotations" value={field.value} onChange={field.onChange} />} />

                  <SectionTitle>Volume Partitions</SectionTitle>
                  <Typography variant="body2" color="text.secondary" sx={{ mb: 1 }}>Mount subdirectories of the PVC at specific container paths. Leave empty to mount the full volume at /data.</Typography>
                  {diskPartitions.map((p, i) => (
                    <Row key={p.id}>
                      <TextField size="small" label="Sub-path" sx={{ flex: 1 }} {...register(`diskPartitions.${i}.subPath`)} />
                      <TextField size="small" label="Mount Path" sx={{ flex: 1 }} {...register(`diskPartitions.${i}.mountPath`)} />
                      <RemoveBtn onClick={() => removePartition(i)} />
                    </Row>
                  ))}
                  <AddBtn label="Add Partition" onClick={() => addPartition({ subPath: '', mountPath: '' })} />
                </>
              )}
            </Box>
          )}

          {/* ── Config ── */}
          {tab === 5 && (
            <Box>
              <SectionTitle>ConfigMaps</SectionTitle>
              {configMaps.map((cm, i) => (
                <Accordion key={cm.id} sx={{ mb: 1 }}>
                  <AccordionSummary expandIcon={<ExpandMoreIcon />}>
                    <Typography variant="body2" sx={{ fontWeight: 500 }}>{watch(`configMaps.${i}.name`) || `ConfigMap ${i + 1}`}</Typography>
                  </AccordionSummary>
                  <AccordionDetails>
                    <Row>
                      <TextField size="small" label="Name *" sx={{ flex: 1 }} {...register(`configMaps.${i}.name`, { required: true })} />
                      <TextField size="small" label="Mount Path *" sx={{ flex: 1 }} {...register(`configMaps.${i}.mountPath`, { required: true })} />
                      <RemoveBtn onClick={() => removeConfigMap(i)} />
                    </Row>
                    <Controller control={control} name={`configMaps.${i}.dataJson`}
                      render={({ field }) => <JsonField label="Data (key: value pairs)" value={field.value} onChange={field.onChange} />} />
                  </AccordionDetails>
                </Accordion>
              ))}
              <AddBtn label="Add ConfigMap" onClick={() => addConfigMap({ name: '', mountPath: '', dataJson: '{}' })} />

              <Divider sx={{ my: 3 }} />
              <SectionTitle>Secrets</SectionTitle>
              {secrets.map((s, i) => (
                <Accordion key={s.id} sx={{ mb: 1 }}>
                  <AccordionSummary expandIcon={<ExpandMoreIcon />}>
                    <Typography variant="body2" sx={{ fontWeight: 500 }}>{watch(`secrets.${i}.name`) || `Secret ${i + 1}`}</Typography>
                  </AccordionSummary>
                  <AccordionDetails>
                    <Row>
                      <TextField size="small" label="Name *" sx={{ flex: 1 }} {...register(`secrets.${i}.name`, { required: true })} />
                      <TextField size="small" label="Mount Path" sx={{ flex: 1 }} {...register(`secrets.${i}.mountPath`)} />
                      <RemoveBtn onClick={() => removeSecret(i)} />
                    </Row>
                    <Row>
                      <TextField size="small" label="Secret Ref (pre-existing)" sx={{ flex: 1 }} {...register(`secrets.${i}.secretRef`)} />
                      <Controller control={control} name={`secrets.${i}.asEnvVars`}
                        render={({ field }) => <FormControlLabel control={<Switch checked={field.value} onChange={e => field.onChange(e.target.checked)} />} label="Inject as Env Vars" />} />
                    </Row>
                    <Controller control={control} name={`secrets.${i}.dataJson`}
                      render={({ field }) => <JsonField label="Inline Data (omit to use secretRef)" value={field.value} onChange={field.onChange} />} />
                  </AccordionDetails>
                </Accordion>
              ))}
              <AddBtn label="Add Secret" onClick={() => addSecret({ name: '', mountPath: '', asEnvVars: false, secretRef: '', dataJson: '' })} />

              <Divider sx={{ my: 3 }} />
              <SectionTitle>External Secrets (ESO)</SectionTitle>
              {externalSecrets.map((es, i) => (
                <Accordion key={es.id} sx={{ mb: 1 }}>
                  <AccordionSummary expandIcon={<ExpandMoreIcon />}>
                    <Typography variant="body2" sx={{ fontWeight: 500 }}>{watch(`externalSecrets.${i}.name`) || `ExternalSecret ${i + 1}`}</Typography>
                  </AccordionSummary>
                  <AccordionDetails>
                    <Row>
                      <TextField size="small" label="Name *" sx={{ flex: 1 }} {...register(`externalSecrets.${i}.name`, { required: true })} />
                      <TextField size="small" label="Store Name *" sx={{ flex: 1 }} {...register(`externalSecrets.${i}.store`, { required: true })} />
                      <TextField size="small" label="Store Kind" select sx={{ flex: 1 }} defaultValue="ClusterSecretStore" {...register(`externalSecrets.${i}.storeKind`)}>
                        <MenuItem value="ClusterSecretStore">ClusterSecretStore</MenuItem>
                        <MenuItem value="SecretStore">SecretStore</MenuItem>
                      </TextField>
                    </Row>
                    <Row>
                      <TextField size="small" label="Refresh Interval" sx={{ flex: 0.8 }} {...register(`externalSecrets.${i}.refreshInterval`)} />
                      <TextField size="small" label="Mount Path" sx={{ flex: 1 }} {...register(`externalSecrets.${i}.mountPath`)} />
                      <Controller control={control} name={`externalSecrets.${i}.asEnvVars`}
                        render={({ field }) => <FormControlLabel control={<Switch checked={field.value} onChange={e => field.onChange(e.target.checked)} />} label="As Env Vars" />} />
                    </Row>
                    <RemoveBtn onClick={() => removeExternalSecret(i)} />
                  </AccordionDetails>
                </Accordion>
              ))}
              <AddBtn label="Add External Secret" onClick={() => addExternalSecret({ name: '', store: '', storeKind: 'ClusterSecretStore', refreshInterval: '1h', mountPath: '', asEnvVars: false, data: [], dataFromJson: '' })} />
            </Box>
          )}

          {/* ── Scaling ── */}
          {tab === 6 && (
            <Box>
              <TextField size="small" label="Replicas" type="number" sx={{ mb: 3, width: 120 }} {...register('replicas')} />
              <Divider sx={{ mb: 3 }} />
              <Controller control={control} name="autoscalingEnabled"
                render={({ field }) => <FormControlLabel control={<Switch checked={field.value} onChange={e => field.onChange(e.target.checked)} />} label="Enable Autoscaling (HPA)" sx={{ mb: 2, display: 'block' }} />} />
              {autoscalingEnabled && (
                <>
                  <Row>
                    <TextField size="small" label="Min Replicas" type="number" sx={{ flex: 1 }} {...register('autoscalingMin')} />
                    <TextField size="small" label="Max Replicas" type="number" sx={{ flex: 1 }} {...register('autoscalingMax')} />
                  </Row>
                  <Row>
                    <TextField size="small" label="Target CPU %" type="number" sx={{ flex: 1 }} slotProps={{ htmlInput: { min: 0, max: 100 } }} {...register('autoscalingCpu')} />
                    <TextField size="small" label="Target Memory % (0 = disabled)" type="number" sx={{ flex: 1 }} slotProps={{ htmlInput: { min: 0, max: 100 } }} {...register('autoscalingMemory')} />
                  </Row>
                </>
              )}
            </Box>
          )}

          {/* ── Advanced ── */}
          {tab === 7 && (
            <Box>
              <SectionTitle>Node Selector</SectionTitle>
              <Controller control={control} name="nodeSelectorsJson"
                render={({ field }) => <JsonField label='Node Selector (e.g. {"kubernetes.io/arch": "arm64"})' value={field.value} onChange={field.onChange} />} />

              <SectionTitle>Tolerations</SectionTitle>
              <Controller control={control} name="tolerationsJson"
                render={({ field }) => <JsonField label="Tolerations (JSON array)" value={field.value} onChange={field.onChange} />} />

              <SectionTitle>Affinity</SectionTitle>
              <Controller control={control} name="affinityJson"
                render={({ field }) => <JsonField label="Affinity (JSON)" value={field.value} onChange={field.onChange} />} />

              <SectionTitle>Pod Security Context</SectionTitle>
              <Controller control={control} name="securityContextJson"
                render={({ field }) => <JsonField label='Security Context (e.g. {"runAsUser": 1000})' value={field.value} onChange={field.onChange} />} />

              <SectionTitle>Lifecycle Hooks</SectionTitle>
              <Controller control={control} name="lifecycleJson"
                render={({ field }) => <JsonField label='Lifecycle (e.g. {"postStart": {"exec": {"command": [...]}}})' value={field.value} onChange={field.onChange} />} />

              <SectionTitle>Logging Config</SectionTitle>
              <Controller control={control} name="loggingConfigJson"
                render={({ field }) => <JsonField label="Logging Config (JSON)" value={field.value} onChange={field.onChange} />} />
            </Box>
          )}
        </Box>
      </Paper>

      <Box sx={{ display: 'flex', justifyContent: 'flex-end', gap: 1, mt: 2 }}>
        <Button variant="outlined" component={Link} to="/">Cancel</Button>
        <Button type="submit" variant="contained" disabled={mutation.isPending}>
          {mutation.isPending ? 'Saving…' : isEdit ? 'Save Changes' : 'Create App'}
        </Button>
      </Box>
    </Box>
  )
}
