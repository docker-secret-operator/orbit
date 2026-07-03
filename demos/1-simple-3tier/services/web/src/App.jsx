import { useState, useEffect } from 'react'

export default function App() {
  const [users, setUsers] = useState([])
  const [products, setProducts] = useState([])
  const [stats, setStats] = useState(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState(null)
  const [refreshCount, setRefreshCount] = useState(0)

  const API_URL = '/api'

  useEffect(() => {
    fetchData()
    const interval = setInterval(fetchData, 5000)
    return () => clearInterval(interval)
  }, [])

  const fetchData = async () => {
    try {
      setLoading(true)
      setError(null)

      const [usersRes, productsRes, statsRes] = await Promise.all([
        fetch(`${API_URL}/users`),
        fetch(`${API_URL}/products`),
        fetch(`${API_URL}/stats`)
      ])

      if (!usersRes.ok || !productsRes.ok || !statsRes.ok) {
        throw new Error('Failed to fetch data')
      }

      const usersData = await usersRes.json()
      const productsData = await productsRes.json()
      const statsData = await statsRes.json()

      setUsers(usersData || [])
      setProducts(productsData || [])
      setStats(statsData)
      setRefreshCount(prev => prev + 1)
    } catch (err) {
      setError(err.message)
    } finally {
      setLoading(false)
    }
  }

  const formatTime = (ms) => {
    if (!ms) return '0ms'
    const seconds = Math.floor(ms / 1000)
    const minutes = Math.floor(seconds / 60)
    const hours = Math.floor(minutes / 60)

    if (hours > 0) return `${hours}h ${minutes % 60}m`
    if (minutes > 0) return `${minutes}m ${seconds % 60}s`
    return `${seconds}s`
  }

  return (
    <div className="container">
      <header>
        <h1>🚀 DPIVOT Demo - 3-Tier Application</h1>
        <p>Real-time demonstration of zero-downtime deployments with Docker</p>
      </header>

      {error && <div className="error">Error: {error}</div>}

      <div className="grid">
        {/* API Status Card */}
        <div className="card">
          <h2>API Status</h2>
          {stats ? (
            <div>
              <div className="stat">
                <span>Version:</span>
                <strong>{stats.version}</strong>
              </div>
              <div className="stat">
                <span>Uptime:</span>
                <strong>{stats.uptime}</strong>
              </div>
              <div className="stat">
                <span>Status:</span>
                <span className="status-badge status-ok">✓ Healthy</span>
              </div>
              <div className="stat">
                <span>Refresh Count:</span>
                <strong>{refreshCount}</strong>
              </div>
            </div>
          ) : (
            <div className="loading">Loading...</div>
          )}
        </div>

        {/* Users Card */}
        <div className="card">
          <h2>Users ({users.length})</h2>
          <div className="list">
            {loading ? (
              <div className="loading">Loading users...</div>
            ) : users.length > 0 ? (
              users.map(user => (
                <div key={user.id} className="list-item">
                  <strong>{user.name}</strong><br />
                  <small>{user.email}</small>
                </div>
              ))
            ) : (
              <div className="loading">No users found</div>
            )}
          </div>
        </div>

        {/* Products Card */}
        <div className="card">
          <h2>Products ({products.length})</h2>
          <div className="list">
            {loading ? (
              <div className="loading">Loading products...</div>
            ) : products.length > 0 ? (
              products.map(product => (
                <div key={product.id} className="list-item">
                  <strong>{product.name}</strong><br />
                  <small>${product.price.toFixed(2)}</small>
                </div>
              ))
            ) : (
              <div className="loading">No products found</div>
            )}
          </div>
        </div>
      </div>

      <div className="card">
        <h2>Manual Actions</h2>
        <div className="btn-group">
          <button onClick={fetchData}>🔄 Refresh Now</button>
          <button onClick={() => window.location.reload()}>↻ Reload Page</button>
        </div>
        <p style={{ color: '#666', fontSize: '12px' }}>
          Auto-refreshing every 5 seconds. Watch this during deployment!
        </p>
      </div>
    </div>
  )
}
