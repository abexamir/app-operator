import { useState } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { useParams, useNavigate, Link } from 'react-router-dom'
import Box from '@mui/material/Box'
import Grid from '@mui/material/Grid'
import Paper from '@mui/material/Paper'
import Typography from '@mui/material/Typography'
import Button from '@mui/material/Button'
import Tabs from '@mui/material/Tabs'
import Tab from '@mui/material/Tab'
import Chip from '@mui/material/Chip'
import Divider from '@mui/material/Divider'
import Alert from '@mui/material/Alert'
import CircularProgress from '@mui/material/CircularProgress'
import Breadcrumbs from '@mui/material/Breadcrumbs'
import Table from '@mui/material/Table'
import TableBody from '@mui/material/TableBody'
import TableCell from '@mui/material/TableCell'
import TableRow from '@mui/material/TableRow'
import Accordion from '@mui/material/Accordion'
import AccordionSummary from '@mui/material/AccordionSummary'
import AccordionDetails from '@mui/material/AccordionDetails'
import NavigateNextIcon from '@mui/icons-material/NavigateNext'
import ExpandMoreIcon from '@mui/icons-material/ExpandMore'
import EditIcon from '@mui/icons-material/Edit'
import DeleteOutlinedIcon from '@mui/icons-material/DeleteOutlined'
import { appdefinitions } from '../api/appdefinitions'
import { StatusChip } from '../components/StatusChip'
import type { ContainerSpec } from '../types/appdefinition'

function age(ts?: string) {
  if (!ts) return '—'
  const s = Math.floor((Date.now() - new Date(ts).getTime()) / 1000)
  if (s < 60) return `${s}s`; if (s < 3600) return `${Math.floor(s / 60)}m`
  if (s < 86400) return `${Math.floor(s / 3600)}h`; return `${Math.floor(s / 86400)}d`
}

function KV({ label, value }: { label: string; value?: React.ReactNode }) {
  if (value === undefined || value === null || value === '') return null
  return (
    <TableRow>
      <TableCell sx={{ width: 200, color: 'text.secondary', fontSize: '0.78rem', py: 0.8, pl: 0, border: 'none' }}>{label}</TableCell>
      <TableCell sx={{ py: 0.8, pr: 0, border: 'none' }}>
        {typeof value === 'string' || typeof value === 'number'
          ? <Typography variant="body2" sx={{ fontFamily: typeof value === 'string' && value.includes(':') ? 'monospace' : 'inherit' }}>{value}</Typography>
          : value}
      </TableCell>
    </TableRow>
  )
}

function Section({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <Box sx={{ mb: 3 }}>
      <Typography variant="subtitle2" color="text.secondary" sx={{ mb: 1.5 }}>{title}</Typography>
      {children}
    </Box>
  )
}

function Code({ children }: { children: string }) {
  return (
    <Box component="pre" sx={{ m: 0, p: 1.5, bgcolor: 'rgba(0,0,0,0.3)', borderRadius: 1, fontSize: '0.75rem', fontFamily: 'monospace', overflow: 'auto', maxHeight: 200, color: 'text.primary', border: '1px solid rgba(255,255,255,0.06)' }}>
      {children}
    </Box>
  )
}

function ContainerCard({ c, idx }: { c: ContainerSpec; idx: number }) {
  return (
    <Accordion defaultExpanded={idx === 0} sx={{ mb: 1 }}>
      <AccordionSummary expandIcon={<ExpandMoreIcon />}>
        <Box sx={{ display: 'flex', alignItems: 'center', gap: 2 }}>
          <Typography variant="body2" sx={{ fontWeight: 600 }}>{c.name}</Typography>
          <Typography variant="body2" sx={{ fontFamily: 'monospace', color: 'primary.light', fontSize: '0.75rem' }}>{c.image}</Typography>
        </Box>
      </AccordionSummary>
      <AccordionDetails>
        <Table size="small"><TableBody>
          {c.command && c.command.length > 0 && <KV label="Command" value={<Code>{c.command.join(' ')}</Code>} />}
          {c.args && c.args.length > 0 && <KV label="Args" value={<Code>{c.args.join(' ')}</Code>} />}
        </TableBody></Table>

        {c.env && c.env.length > 0 && (
          <Box sx={{ mt: 1.5 }}>
            <Typography variant="caption" color="text.secondary" sx={{ mb: 0.5, display: 'block' }}>Environment Variables</Typography>
            <Box sx={{ display: 'flex', flexWrap: 'wrap', gap: 0.5 }}>
              {c.env.map(e => (
                <Chip key={e.name} size="small" variant="outlined"
                  label={<><strong>{e.name}</strong>{e.value ? `=${e.value}` : ' (from ref)'}</>}
                  sx={{ fontFamily: 'monospace', fontSize: '0.72rem', height: 24 }} />
              ))}
            </Box>
          </Box>
        )}

        {c.ports && c.ports.length > 0 && (
          <Box sx={{ mt: 1.5 }}>
            <Typography variant="caption" color="text.secondary" sx={{ mb: 0.5, display: 'block' }}>Ports</Typography>
            <Box sx={{ display: 'flex', flexWrap: 'wrap', gap: 0.5 }}>
              {c.ports.map(p => (
                <Chip key={p.name} size="small" variant="outlined" color={p.metrics?.enabled ? 'secondary' : 'default'}
                  label={`${p.name} :${p.containerPort}→${p.servicePort}${p.metrics?.enabled ? ' 📊' : ''}`}
                  sx={{ fontFamily: 'monospace', fontSize: '0.72rem', height: 24 }} />
              ))}
            </Box>
          </Box>
        )}

        {c.resources && (
          <Box sx={{ mt: 1.5 }}>
            <Typography variant="caption" color="text.secondary" sx={{ mb: 0.5, display: 'block' }}>Resources</Typography>
            <Table size="small"><TableBody>
              {c.resources.requests && <KV label="Requests" value={JSON.stringify(c.resources.requests)} />}
              {c.resources.limits && <KV label="Limits" value={JSON.stringify(c.resources.limits)} />}
            </TableBody></Table>
          </Box>
        )}

        {(c.readinessProbe || c.livenessProbe) && (
          <Box sx={{ mt: 1.5 }}>
            <Typography variant="caption" color="text.secondary" sx={{ mb: 0.5, display: 'block' }}>Probes</Typography>
            <Table size="small"><TableBody>
              {c.readinessProbe && <KV label="Readiness" value={<Code>{JSON.stringify(c.readinessProbe, null, 2)}</Code>} />}
              {c.livenessProbe && <KV label="Liveness" value={<Code>{JSON.stringify(c.livenessProbe, null, 2)}</Code>} />}
            </TableBody></Table>
          </Box>
        )}
      </AccordionDetails>
    </Accordion>
  )
}

const TABS = ['Overview', 'Containers', 'Network', 'Storage', 'Config', 'Scaling', 'Advanced']

export function AppDetail() {
  const { namespace, name } = useParams<{ namespace: string; name: string }>()
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const [tab, setTab] = useState(0)

  const { data: app, isLoading, error } = useQuery({
    queryKey: ['appdefinitions', namespace, name],
    queryFn: () => appdefinitions.get(namespace!, name!),
    refetchInterval: 10_000,
  })

  const del = useMutation({
    mutationFn: () => appdefinitions.delete(namespace!, name!),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['appdefinitions'] })
      navigate('/')
    },
  })

  if (isLoading) return <Box sx={{ p: 4 }}><CircularProgress size={28} /></Box>
  if (error) return <Box sx={{ p: 4 }}><Alert severity="error">{String(error)}</Alert></Box>
  if (!app) return null

  const s = app.spec

  return (
    <Box sx={{ p: 4 }}>
      <Breadcrumbs separator={<NavigateNextIcon fontSize="small" />} sx={{ mb: 2 }}>
        <Typography component={Link} to="/" variant="body2" sx={{ color: 'text.secondary', textDecoration: 'none', '&:hover': { color: 'text.primary' } }}>Applications</Typography>
        <Typography variant="body2" color="text.primary">{name}</Typography>
      </Breadcrumbs>

      <Box sx={{ display: 'flex', alignItems: 'flex-start', justifyContent: 'space-between', mb: 3 }}>
        <Box>
          <Box sx={{ display: 'flex', alignItems: 'center', gap: 1.5, mb: 0.5 }}>
            <Typography variant="h5">{app.metadata.name}</Typography>
            {s.paused && <Chip label="Paused" color="warning" size="small" />}
          </Box>
          <Typography variant="body2" color="text.secondary">{app.metadata.namespace}</Typography>
        </Box>
        <Box sx={{ display: 'flex', gap: 1 }}>
          <Button variant="outlined" size="small" startIcon={<EditIcon />}
            onClick={() => navigate(`/namespaces/${namespace}/apps/${name}/edit`)}>
            Edit
          </Button>
          <Button variant="outlined" size="small" color="error" startIcon={<DeleteOutlinedIcon />}
            onClick={() => { if (confirm(`Delete ${name}?`)) del.mutate() }}>
            Delete
          </Button>
        </Box>
      </Box>

      {/* Status cards */}
      <Grid container spacing={2} sx={{ mb: 3 }}>
        {[
          { label: 'Phase', value: <StatusChip phase={app.status?.phase} /> },
          { label: 'Ready / Desired', value: `${app.status?.readyReplicas ?? 0} / ${app.status?.replicas ?? 1}` },
          { label: 'Age', value: age(app.metadata.creationTimestamp) },
          { label: 'Generation', value: app.status?.observedGeneration ?? '—' },
        ].map(c => (
          <Grid key={c.label} size={{ xs: 6, sm: 3 }}>
            <Paper sx={{ p: 2 }}>
              <Typography variant="caption" color="text.secondary" sx={{ mb: 0.5, display: 'block' }}>{c.label}</Typography>
              {typeof c.value === 'string' || typeof c.value === 'number'
                ? <Typography variant="body2" sx={{ fontWeight: 600 }}>{c.value}</Typography>
                : c.value}
            </Paper>
          </Grid>
        ))}
      </Grid>

      {app.status?.lastError && (
        <Alert severity="error" sx={{ mb: 2 }}>{app.status.lastError}</Alert>
      )}

      {/* Conditions */}
      {app.status?.conditions && app.status.conditions.length > 0 && (
        <Paper sx={{ mb: 3, p: 2 }}>
          <Typography variant="subtitle2" color="text.secondary" sx={{ mb: 1.5 }}>Conditions</Typography>
          <Table size="small"><TableBody>
            {app.status.conditions.map(c => (
              <TableRow key={c.type}>
                <TableCell sx={{ border: 'none', py: 0.6, pl: 0, width: 160, color: 'text.secondary', fontSize: '0.78rem' }}>{c.type}</TableCell>
                <TableCell sx={{ border: 'none', py: 0.6, width: 70 }}>
                  <Chip size="small" label={c.status} color={c.status === 'True' ? 'success' : c.status === 'False' ? 'error' : 'default'} variant="outlined" />
                </TableCell>
                <TableCell sx={{ border: 'none', py: 0.6, color: 'text.secondary', fontSize: '0.78rem' }}>{c.message}</TableCell>
              </TableRow>
            ))}
          </TableBody></Table>
        </Paper>
      )}

      <Paper sx={{ mb: 0 }}>
        <Tabs value={tab} onChange={(_, v) => setTab(v)} sx={{ borderBottom: 1, borderColor: 'divider', px: 2 }}>
          {TABS.map(t => <Tab key={t} label={t} />)}
        </Tabs>

        <Box sx={{ p: 3 }}>
          {/* Overview */}
          {tab === 0 && (
            <Table size="small"><TableBody>
              <KV label="Replicas" value={s.replicas ?? 1} />
              <KV label="Service Type" value={s.serviceType || 'ClusterIP'} />
              <KV label="Ingress Class" value={s.ingressClass} />
              <KV label="Paused" value={s.paused ? 'Yes' : 'No'} />
              {s.imagePullSecrets && s.imagePullSecrets.length > 0 && (
                <KV label="Image Pull Secrets" value={
                  <Box sx={{ display: 'flex', gap: 0.5, flexWrap: 'wrap' }}>
                    {s.imagePullSecrets.map(p => <Chip key={p.name} size="small" label={p.name} variant="outlined" />)}
                  </Box>
                } />
              )}
              {s.nodeSelector && Object.keys(s.nodeSelector).length > 0 && (
                <KV label="Node Selector" value={
                  <Box sx={{ display: 'flex', gap: 0.5, flexWrap: 'wrap' }}>
                    {Object.entries(s.nodeSelector).map(([k, v]) => <Chip key={k} size="small" label={`${k}=${v}`} variant="outlined" sx={{ fontFamily: 'monospace', fontSize: '0.72rem' }} />)}
                  </Box>
                } />
              )}
              {s.ingressAnnotations && Object.keys(s.ingressAnnotations).length > 0 && (
                <KV label="Ingress Annotations" value={<Code>{JSON.stringify(s.ingressAnnotations, null, 2)}</Code>} />
              )}
            </TableBody></Table>
          )}

          {/* Containers */}
          {tab === 1 && (
            <Box>
              <Section title="Init Containers">
                {(!s.initContainers || s.initContainers.length === 0)
                  ? <Typography variant="body2" color="text.secondary">None</Typography>
                  : s.initContainers.map((c, i) => <ContainerCard key={c.name} c={c as ContainerSpec} idx={i} />)}
              </Section>
              <Divider sx={{ my: 2 }} />
              <Section title="Main Containers">
                {(s.containers ?? []).map((c, i) => <ContainerCard key={c.name} c={c} idx={i} />)}
              </Section>
            </Box>
          )}

          {/* Network */}
          {tab === 2 && (
            <Box>
              <Section title="Service">
                <Table size="small"><TableBody>
                  <KV label="Type" value={s.serviceType || 'ClusterIP'} />
                </TableBody></Table>
              </Section>
              <Section title="Domains / Ingress">
                {(!s.domains || s.domains.length === 0)
                  ? <Typography variant="body2" color="text.secondary">No ingress configured</Typography>
                  : s.domains.map(d => (
                    <Paper key={d.name} sx={{ p: 2, mb: 1 }}>
                      <Table size="small"><TableBody>
                        <KV label="Host" value={d.name} />
                        <KV label="TLS" value={d.tls ? 'Enabled' : 'Disabled'} />
                        {d.tls && <KV label="Redirect HTTP→HTTPS" value={d.redirect_tls ? 'Yes' : 'No'} />}
                        <KV label="Cert Issuer" value={d.certIssuer} />
                        <KV label="Path" value={d.path || '/'} />
                        <KV label="Port Name" value={d.portName || 'http'} />
                        <KV label="TLS Secret" value={d.secretName} />
                        {d.annotations && Object.keys(d.annotations).length > 0 && (
                          <KV label="Annotations" value={<Code>{JSON.stringify(d.annotations, null, 2)}</Code>} />
                        )}
                      </TableBody></Table>
                    </Paper>
                  ))}
              </Section>
            </Box>
          )}

          {/* Storage */}
          {tab === 3 && (
            <Box>
              {!s.disk
                ? <Typography variant="body2" color="text.secondary">No persistent disk configured.</Typography>
                : (
                  <Section title="Persistent Disk">
                    <Table size="small"><TableBody>
                      <KV label="Size" value={`${s.disk.sizeInGi} Gi`} />
                      <KV label="Storage Class" value={s.disk.storageClassName || '(cluster default)'} />
                      <KV label="Set fsGroup" value={s.disk.setFsGroup ? 'Yes' : 'No'} />
                      <KV label="Protect" value={s.disk.protect ? 'Yes' : 'No'} />
                      {s.disk.annotations && Object.keys(s.disk.annotations).length > 0 && (
                        <KV label="Annotations" value={<Code>{JSON.stringify(s.disk.annotations, null, 2)}</Code>} />
                      )}
                    </TableBody></Table>
                    {s.disk.partitions && s.disk.partitions.length > 0 && (
                      <Box sx={{ mt: 2 }}>
                        <Typography variant="caption" color="text.secondary" sx={{ mb: 0.5, display: 'block' }}>Partitions</Typography>
                        {s.disk.partitions.map(p => (
                          <Paper key={p.subPath} sx={{ p: 1.5, mb: 1 }}>
                            <Table size="small"><TableBody>
                              <KV label="Sub-path" value={p.subPath} />
                              <KV label="Mount path" value={p.mountPath} />
                            </TableBody></Table>
                          </Paper>
                        ))}
                      </Box>
                    )}
                  </Section>
                )}
            </Box>
          )}

          {/* Config */}
          {tab === 4 && (
            <Box>
              <Section title="ConfigMaps">
                {(!s.configMaps || s.configMaps.length === 0)
                  ? <Typography variant="body2" color="text.secondary">None</Typography>
                  : s.configMaps.map(cm => (
                    <Accordion key={cm.name} sx={{ mb: 1 }}>
                      <AccordionSummary expandIcon={<ExpandMoreIcon />}>
                        <Typography variant="body2" sx={{ fontWeight: 600 }}>{cm.name}</Typography>
                        <Typography variant="body2" color="text.secondary" sx={{ ml: 2 }}>{cm.mountPath}</Typography>
                      </AccordionSummary>
                      <AccordionDetails>
                        <Code>{JSON.stringify(cm.data, null, 2)}</Code>
                      </AccordionDetails>
                    </Accordion>
                  ))}
              </Section>

              <Section title="Secrets">
                {(!s.secrets || s.secrets.length === 0)
                  ? <Typography variant="body2" color="text.secondary">None</Typography>
                  : s.secrets.map(sec => (
                    <Paper key={sec.name} sx={{ p: 2, mb: 1 }}>
                      <Table size="small"><TableBody>
                        <KV label="Name" value={sec.name} />
                        <KV label="Mount Path" value={sec.mountPath} />
                        <KV label="As Env Vars" value={sec.asEnvVars ? 'Yes' : undefined} />
                        <KV label="Secret Ref" value={sec.secretRef} />
                        {sec.data && <KV label="Keys" value={Object.keys(sec.data).join(', ')} />}
                      </TableBody></Table>
                    </Paper>
                  ))}
              </Section>

              <Section title="External Secrets">
                {(!s.externalSecrets || s.externalSecrets.length === 0)
                  ? <Typography variant="body2" color="text.secondary">None</Typography>
                  : s.externalSecrets.map(es => (
                    <Paper key={es.name} sx={{ p: 2, mb: 1 }}>
                      <Table size="small"><TableBody>
                        <KV label="Name" value={es.name} />
                        <KV label="Store" value={`${es.storeKind ?? 'ClusterSecretStore'} / ${es.store}`} />
                        <KV label="Refresh Interval" value={es.refreshInterval} />
                        <KV label="Mount Path" value={es.mountPath} />
                        <KV label="As Env Vars" value={es.asEnvVars ? 'Yes' : undefined} />
                        {es.data && es.data.length > 0 && (
                          <KV label="Data" value={<Code>{JSON.stringify(es.data, null, 2)}</Code>} />
                        )}
                        {es.dataFrom && es.dataFrom.length > 0 && (
                          <KV label="Data From" value={<Code>{JSON.stringify(es.dataFrom, null, 2)}</Code>} />
                        )}
                      </TableBody></Table>
                    </Paper>
                  ))}
              </Section>
            </Box>
          )}

          {/* Scaling */}
          {tab === 5 && (
            <Box>
              <Section title="Replicas">
                <Table size="small"><TableBody>
                  <KV label="Desired" value={s.replicas ?? 1} />
                  <KV label="Ready" value={app.status?.readyReplicas ?? 0} />
                </TableBody></Table>
              </Section>
              <Section title="Autoscaling">
                {!s.autoscaling
                  ? <Typography variant="body2" color="text.secondary">Not configured</Typography>
                  : (
                    <Table size="small"><TableBody>
                      <KV label="Enabled" value={s.autoscaling.enabled ? 'Yes' : 'No'} />
                      <KV label="Min Replicas" value={s.autoscaling.minReplicas} />
                      <KV label="Max Replicas" value={s.autoscaling.maxReplicas} />
                      <KV label="Target CPU %" value={s.autoscaling.targetCPUUtilizationPercentage} />
                      <KV label="Target Memory %" value={s.autoscaling.targetMemoryUtilizationPercentage} />
                    </TableBody></Table>
                  )}
              </Section>
            </Box>
          )}

          {/* Advanced */}
          {tab === 6 && (
            <Box>
              <Section title="Lifecycle Hooks">
                {!s.lifecycle
                  ? <Typography variant="body2" color="text.secondary">None</Typography>
                  : (
                    <Table size="small"><TableBody>
                      {s.lifecycle.postStart && <KV label="Post Start" value={<Code>{JSON.stringify(s.lifecycle.postStart, null, 2)}</Code>} />}
                      {s.lifecycle.preStop && <KV label="Pre Stop" value={<Code>{JSON.stringify(s.lifecycle.preStop, null, 2)}</Code>} />}
                    </TableBody></Table>
                  )}
              </Section>
              <Section title="Security Context">
                {!s.securityContext
                  ? <Typography variant="body2" color="text.secondary">None</Typography>
                  : <Code>{JSON.stringify(s.securityContext, null, 2)}</Code>}
              </Section>
              <Section title="Node Selector">
                {(!s.nodeSelector || Object.keys(s.nodeSelector).length === 0)
                  ? <Typography variant="body2" color="text.secondary">None</Typography>
                  : <Code>{JSON.stringify(s.nodeSelector, null, 2)}</Code>}
              </Section>
              <Section title="Tolerations">
                {(!s.tolerations || s.tolerations.length === 0)
                  ? <Typography variant="body2" color="text.secondary">None</Typography>
                  : <Code>{JSON.stringify(s.tolerations, null, 2)}</Code>}
              </Section>
              <Section title="Affinity">
                {!s.affinity
                  ? <Typography variant="body2" color="text.secondary">None</Typography>
                  : <Code>{JSON.stringify(s.affinity, null, 2)}</Code>}
              </Section>
              <Section title="Logging Config">
                {!s.loggingConfig
                  ? <Typography variant="body2" color="text.secondary">None</Typography>
                  : <Code>{JSON.stringify(s.loggingConfig, null, 2)}</Code>}
              </Section>
            </Box>
          )}
        </Box>
      </Paper>
    </Box>
  )
}
