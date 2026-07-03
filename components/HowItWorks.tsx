import styles from "./HowItWorks.module.css";

const steps = [
  "Start a second container for the service, alongside the running one.",
  "Wait for its health check to pass — an unhealthy build never takes traffic.",
  "Register the new container with the proxy over its control API.",
  "Stop routing new connections to the old container.",
  "Let in-flight requests finish draining.",
  "Stop and remove the old container.",
];

export default function HowItWorks() {
  return (
    <section className="section" id="how" aria-labelledby="how-title">
      <div className="container">
        <p className="eyebrow">How it works</p>
        <h2 id="how-title" className="section-title">
          A permanent proxy in front of a replaceable backend.
        </h2>
        <p className="section-lead">
          <code>docker orbit generate</code> rewrites your stack so a tiny
          built-in TCP proxy owns the host port for good. Your service becomes a
          backend behind it. Deploys swap the backend; the port never moves.
        </p>

        <div className={styles.diagramWrap}>
          <svg
            className={styles.diagram}
            viewBox="0 0 720 300"
            role="img"
            aria-labelledby="how-diagram-title how-diagram-desc"
          >
            <title id="how-diagram-title">Orbit rollout data flow</title>
            <desc id="how-diagram-desc">
              A client connects to the Orbit proxy, which permanently owns the
              host port. Live traffic is routed to the new healthy container
              while the old container drains before removal.
            </desc>
            <defs>
              <marker
                id="arrow"
                viewBox="0 0 10 10"
                refX="8"
                refY="5"
                markerWidth="7"
                markerHeight="7"
                orient="auto-start-reverse"
              >
                <path d="M0 1 L9 5 L0 9" fill="var(--accent)" />
              </marker>
              <marker
                id="arrow-muted"
                viewBox="0 0 10 10"
                refX="8"
                refY="5"
                markerWidth="7"
                markerHeight="7"
                orient="auto-start-reverse"
              >
                <path d="M0 1 L9 5 L0 9" fill="var(--muted)" />
              </marker>
            </defs>

            {/* Client */}
            <g className={styles.node}>
              <rect x="18" y="118" width="128" height="64" rx="8" />
              <text x="82" y="145" className={styles.nLabel}>
                Client
              </text>
              <text x="82" y="166" className={styles.nSub}>
                :3000
              </text>
            </g>

            {/* Proxy */}
            <g>
              <rect
                x="248"
                y="104"
                width="168"
                height="92"
                rx="10"
                className={styles.proxy}
              />
              <text x="332" y="138" className={styles.nLabelAccent}>
                Orbit proxy
              </text>
              <text x="332" y="160" className={styles.nSub}>
                owns :3000
              </text>
              <text x="332" y="178" className={styles.nSubSmall}>
                permanent
              </text>
            </g>

            {/* New backend */}
            <g className={styles.nodeActive}>
              <rect x="520" y="58" width="182" height="64" rx="8" />
              <circle cx="546" cy="90" r="5" className={styles.health} />
              <text x="562" y="84" className={styles.nLabelLeft}>
                web · 2.0.0
              </text>
              <text x="562" y="104" className={styles.nSubLeft}>
                healthy · live
              </text>
            </g>

            {/* Old backend */}
            <g className={styles.nodeDrain}>
              <rect
                x="520"
                y="178"
                width="182"
                height="64"
                rx="8"
                strokeDasharray="5 4"
              />
              <text x="536" y="204" className={styles.nLabelLeftMuted}>
                web · 1.0.0
              </text>
              <text x="536" y="224" className={styles.nSubLeft}>
                draining
              </text>
            </g>

            {/* Edges */}
            <line
              x1="146"
              y1="150"
              x2="242"
              y2="150"
              className={styles.edge}
              markerEnd="url(#arrow)"
            />
            <path
              d="M416 140 C 470 120, 480 100, 514 92"
              className={styles.edge}
              fill="none"
              markerEnd="url(#arrow)"
            />
            <path
              d="M416 160 C 470 190, 480 200, 514 208"
              className={styles.edgeMuted}
              fill="none"
              markerEnd="url(#arrow-muted)"
            />

            <text x="452" y="96" className={styles.edgeLabel}>
              live traffic
            </text>
            <text x="446" y="208" className={styles.edgeLabelMuted}>
              no new conns
            </text>
          </svg>
        </div>

        <ol className={styles.steps}>
          {steps.map((s, i) => (
            <li key={i} className={styles.step}>
              <span className={styles.num}>{i + 1}</span>
              <span>{s}</span>
            </li>
          ))}
        </ol>
      </div>
    </section>
  );
}
