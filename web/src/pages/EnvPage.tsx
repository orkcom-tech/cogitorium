import EnvPanel from './EnvPanel'

// The install-wide named values. Administrators only, on the same reasoning as
// gear approval and deletion: one name here reaches every workspace on this
// install, so it is an administrator's decision and not a member's. A
// workspace's own overrides live on the workspace, next to the agents that use
// them.
export default function EnvPage() {
  return (
    <div className="page">
      <h2>Variables &amp; Secrets</h2>
      <EnvPanel />
    </div>
  )
}
