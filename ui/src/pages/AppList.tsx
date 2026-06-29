import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { useNavigate } from 'react-router-dom'
import Box from '@mui/material/Box'
import Button from '@mui/material/Button'
import Typography from '@mui/material/Typography'
import IconButton from '@mui/material/IconButton'
import Tooltip from '@mui/material/Tooltip'
import Paper from '@mui/material/Paper'
import Alert from '@mui/material/Alert'
import CircularProgress from '@mui/material/CircularProgress'
import { DataGrid, type GridColDef, type GridRenderCellParams } from '@mui/x-data-grid'
import AddIcon from '@mui/icons-material/Add'
import OpenInNewIcon from '@mui/icons-material/OpenInNew'
import EditIcon from '@mui/icons-material/Edit'
import DeleteOutlinedIcon from '@mui/icons-material/DeleteOutlined'
import { appdefinitions } from '../api/appdefinitions'
import { StatusChip } from '../components/StatusChip'

function age(ts?: string) {
  if (!ts) return '—'
  const s = Math.floor((Date.now() - new Date(ts).getTime()) / 1000)
  if (s < 60) return `${s}s`
  if (s < 3600) return `${Math.floor(s / 60)}m`
  if (s < 86400) return `${Math.floor(s / 3600)}h`
  return `${Math.floor(s / 86400)}d`
}

export function AppList() {
  const navigate = useNavigate()
  const queryClient = useQueryClient()

  const { data, isLoading, error } = useQuery({
    queryKey: ['appdefinitions'],
    queryFn: () => appdefinitions.list(),
    refetchInterval: 10_000,
  })

  const del = useMutation({
    mutationFn: ({ namespace, name }: { namespace: string; name: string }) =>
      appdefinitions.delete(namespace, name),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ['appdefinitions'] }),
  })

  const rows = (data?.items ?? []).map(app => ({
    id: `${app.metadata.namespace}/${app.metadata.name}`,
    name: app.metadata.name,
    namespace: app.metadata.namespace,
    phase: app.status?.phase,
    ready: app.status?.readyReplicas ?? 0,
    replicas: app.status?.replicas ?? app.spec.replicas ?? 1,
    image: app.spec.containers?.[0]?.image ?? '—',
    age: age(app.metadata.creationTimestamp),
    paused: app.spec.paused,
    _raw: app,
  }))

  const columns: GridColDef[] = [
    {
      field: 'name',
      headerName: 'Name',
      flex: 1.2,
      renderCell: ({ row }: GridRenderCellParams) => (
        <Typography
          variant="body2"
          sx={{ fontWeight: 600, color: 'primary.light', cursor: 'pointer', '&:hover': { textDecoration: 'underline' } }}
          onClick={() => navigate(`/namespaces/${row.namespace}/apps/${row.name}`)}
        >
          {row.name}
        </Typography>
      ),
    },
    {
      field: 'namespace',
      headerName: 'Namespace',
      flex: 0.8,
      renderCell: ({ value }: GridRenderCellParams) => (
        <Typography variant="body2" sx={{ color: 'text.secondary' }}>{value}</Typography>
      ),
    },
    {
      field: 'phase',
      headerName: 'Status',
      width: 130,
      renderCell: ({ row }: GridRenderCellParams) => <StatusChip phase={row.phase} />,
    },
    {
      field: 'ready',
      headerName: 'Ready',
      width: 90,
      renderCell: ({ row }: GridRenderCellParams) => (
        <Typography variant="body2" sx={{ fontFamily: 'monospace', color: row.ready === row.replicas ? 'success.main' : 'warning.main' }}>
          {row.ready}/{row.replicas}
        </Typography>
      ),
    },
    {
      field: 'image',
      headerName: 'Image',
      flex: 1.5,
      renderCell: ({ value }: GridRenderCellParams) => (
        <Typography variant="body2" sx={{ fontFamily: 'monospace', fontSize: '0.75rem', color: 'text.secondary' }}>{value}</Typography>
      ),
    },
    { field: 'age', headerName: 'Age', width: 80 },
    {
      field: 'actions',
      headerName: '',
      width: 110,
      sortable: false,
      renderCell: ({ row }: GridRenderCellParams) => (
        <Box sx={{ display: 'flex', gap: 0.5 }}>
          <Tooltip title="View">
            <IconButton size="small" onClick={() => navigate(`/namespaces/${row.namespace}/apps/${row.name}`)}>
              <OpenInNewIcon sx={{ fontSize: 16 }} />
            </IconButton>
          </Tooltip>
          <Tooltip title="Edit">
            <IconButton size="small" onClick={() => navigate(`/namespaces/${row.namespace}/apps/${row.name}/edit`)}>
              <EditIcon sx={{ fontSize: 16 }} />
            </IconButton>
          </Tooltip>
          <Tooltip title="Delete">
            <IconButton
              size="small"
              color="error"
              onClick={() => {
                if (!confirm(`Delete ${row.name}?`)) return
                del.mutate({ namespace: row.namespace, name: row.name })
              }}
            >
              <DeleteOutlinedIcon sx={{ fontSize: 16 }} />
            </IconButton>
          </Tooltip>
        </Box>
      ),
    },
  ]

  return (
    <Box sx={{ p: 4 }}>
      <Box sx={{ display: 'flex', alignItems: 'flex-start', justifyContent: 'space-between', mb: 3 }}>
        <Box>
          <Typography variant="h5" gutterBottom>Applications</Typography>
          <Typography variant="body2" color="text.secondary">
            {rows.length} app{rows.length !== 1 ? 's' : ''} across all namespaces
          </Typography>
        </Box>
        <Button variant="contained" startIcon={<AddIcon />} onClick={() => navigate('/new')}>
          New App
        </Button>
      </Box>

      {error && <Alert severity="error" sx={{ mb: 2 }}>{String(error)}</Alert>}

      <Paper sx={{ height: 520 }}>
        <DataGrid
          rows={rows}
          columns={columns}
          loading={isLoading}
          disableRowSelectionOnClick
          pageSizeOptions={[25, 50]}
          initialState={{ pagination: { paginationModel: { pageSize: 25 } } }}
          sx={{
            border: 'none',
            '& .MuiDataGrid-columnHeaders': { bgcolor: 'rgba(255,255,255,0.02)' },
            '& .MuiDataGrid-row:hover': { bgcolor: 'rgba(124,106,247,0.05)' },
            '& .MuiDataGrid-cell': { borderColor: 'rgba(255,255,255,0.04)' },
            '& .MuiDataGrid-footerContainer': { borderTop: '1px solid rgba(255,255,255,0.05)' },
          }}
          slots={{
            noRowsOverlay: () => (
              <Box sx={{ display: 'flex', alignItems: 'center', justifyContent: 'center', height: '100%', flexDirection: 'column', gap: 1 }}>
                {isLoading
                  ? <CircularProgress size={28} />
                  : <Typography color="text.secondary">No applications found</Typography>}
              </Box>
            ),
          }}
        />
      </Paper>
    </Box>
  )
}
