import { BrowserRouter, Routes, Route } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { ThemeProvider, CssBaseline } from '@mui/material'
import { theme } from './theme'
import { Layout } from './components/Layout'
import { AppList } from './pages/AppList'
import { AppDetail } from './pages/AppDetail'
import { AppForm } from './pages/AppForm'

const queryClient = new QueryClient({
  defaultOptions: { queries: { retry: 1, staleTime: 5_000 } },
})

export default function App() {
  return (
    <ThemeProvider theme={theme}>
      <CssBaseline />
      <QueryClientProvider client={queryClient}>
        <BrowserRouter>
          <Layout>
            <Routes>
              <Route path="/" element={<AppList />} />
              <Route path="/new" element={<AppForm />} />
              <Route path="/namespaces/:namespace/apps/:name" element={<AppDetail />} />
              <Route path="/namespaces/:namespace/apps/:name/edit" element={<AppForm />} />
            </Routes>
          </Layout>
        </BrowserRouter>
      </QueryClientProvider>
    </ThemeProvider>
  )
}
