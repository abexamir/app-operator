import { useState, type FormEvent } from 'react'
import Box from '@mui/material/Box'
import Button from '@mui/material/Button'
import Paper from '@mui/material/Paper'
import TextField from '@mui/material/TextField'
import Typography from '@mui/material/Typography'
import LockOutlinedIcon from '@mui/icons-material/LockOutlined'
import { getAccessToken, setAccessToken, setActiveNamespace } from '../api/client'

export function AuthGate({ children }: { children: React.ReactNode }) {
  const [authenticated, setAuthenticated] = useState(() => Boolean(getAccessToken()))
  const [token, setToken] = useState('')
  const [namespace, setNamespace] = useState('default')

  if (authenticated) return children

  const submit = (event: FormEvent) => {
    event.preventDefault()
    if (!token.trim()) return
    setAccessToken(token)
    setActiveNamespace(namespace)
    setAuthenticated(true)
  }

  return (
    <Box sx={{ minHeight: '100vh', display: 'grid', placeItems: 'center', bgcolor: 'background.default', p: 3 }}>
      <Paper component="form" onSubmit={submit} sx={{ width: '100%', maxWidth: 460, p: 4 }}>
        <LockOutlinedIcon color="primary" sx={{ mb: 2 }} />
        <Typography variant="h5" gutterBottom>Authenticate</Typography>
        <Typography variant="body2" color="text.secondary" sx={{ mb: 3 }}>
          Enter a short-lived Kubernetes bearer token. Access is limited by the token owner&apos;s Kubernetes RBAC permissions.
        </Typography>
        <TextField
          label="Kubernetes namespace"
          value={namespace}
          onChange={event => setNamespace(event.target.value)}
          autoComplete="off"
          fullWidth
          required
          sx={{ mb: 2 }}
        />
        <TextField
          label="Bearer token"
          type="password"
          value={token}
          onChange={event => setToken(event.target.value)}
          autoComplete="off"
          fullWidth
          required
          sx={{ mb: 2 }}
        />
        <Button type="submit" variant="contained" fullWidth>Continue</Button>
      </Paper>
    </Box>
  )
}
