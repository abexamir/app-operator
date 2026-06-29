import { Link, useLocation } from 'react-router-dom'
import Box from '@mui/material/Box'
import Drawer from '@mui/material/Drawer'
import List from '@mui/material/List'
import ListItemButton from '@mui/material/ListItemButton'
import ListItemIcon from '@mui/material/ListItemIcon'
import ListItemText from '@mui/material/ListItemText'
import Typography from '@mui/material/Typography'
import Divider from '@mui/material/Divider'
import AppsIcon from '@mui/icons-material/Apps'
import HexagonOutlinedIcon from '@mui/icons-material/HexagonOutlined'

const DRAWER_WIDTH = 220

const navItems = [
  { label: 'Applications', path: '/', icon: <AppsIcon sx={{ fontSize: 18 }} /> },
]

export function Layout({ children }: { children: React.ReactNode }) {
  const location = useLocation()

  return (
    <Box sx={{ display: 'flex', minHeight: '100vh', bgcolor: 'background.default' }}>
      <Drawer
        variant="permanent"
        sx={{
          width: DRAWER_WIDTH,
          flexShrink: 0,
          '& .MuiDrawer-paper': { width: DRAWER_WIDTH, bgcolor: 'background.paper' },
        }}
      >
        <Box sx={{ px: 2, py: 2.5, display: 'flex', alignItems: 'center', gap: 1.5 }}>
          <HexagonOutlinedIcon sx={{ color: 'primary.main', fontSize: 22 }} />
          <Typography sx={{ color: 'text.primary', fontWeight: 600, fontSize: '0.85rem', letterSpacing: '0.04em' }}>
            App Operator
          </Typography>
        </Box>
        <Divider />
        <List sx={{ pt: 1.5 }}>
          {navItems.map(item => (
            <ListItemButton
              key={item.path}
              component={Link}
              to={item.path}
              selected={location.pathname === item.path}
            >
              <ListItemIcon sx={{ minWidth: 32, color: 'inherit' }}>{item.icon}</ListItemIcon>
              <ListItemText
                primary={item.label}
                slotProps={{ primary: { style: { fontSize: '0.85rem', fontWeight: 500 } } }}
              />
            </ListItemButton>
          ))}
        </List>
      </Drawer>

      <Box component="main" sx={{ flexGrow: 1, overflow: 'auto', minHeight: '100vh' }}>
        {children}
      </Box>
    </Box>
  )
}
