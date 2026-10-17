import { useRoute } from './router'
import { StoreProvider, useStore } from './store'
import { Shell } from './ui/Shell'
import { ChangesPage } from './pages/ChangesPage'
import { ChangePage } from './pages/ChangePage'
import { SignInPage } from './pages/SignInPage'
import { AlertsPage } from './pages/AlertsPage'
import { ReposPage } from './pages/ReposPage'
import { PipelinesPage } from './pages/PipelinesPage'
import { DesignPage } from './pages/DesignPage'
import { SettingsPage } from './pages/SettingsPage'

export function App() {
  return (
    <StoreProvider>
      <Routed />
    </StoreProvider>
  )
}

function Routed() {
  const [route] = useRoute()
  const { state } = useStore()
  if (route.screen === 'login' || state.signedOut) return <SignInPage />
  return (
    <Shell>
      {route.screen === 'changes' && <ChangesPage />}
      {route.screen === 'change' && <ChangePage id={route.id} />}
      {route.screen === 'alerts' && <AlertsPage />}
      {route.screen === 'repos' && <ReposPage />}
      {route.screen === 'pipelines' && <PipelinesPage repoId={route.repoId} />}
      {route.screen === 'design' && <DesignPage />}
      {route.screen === 'settings' && <SettingsPage />}
    </Shell>
  )
}
