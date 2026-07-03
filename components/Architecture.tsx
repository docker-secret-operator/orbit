import { site } from "@/lib/site";
import { ExternalIcon } from "./icons";
import styles from "./Architecture.module.css";

const layers = [
  { label: "docker orbit ‹cmd›", sub: "you", accent: false },
  { label: "Docker CLI plugin", sub: "docker-orbit binary", accent: false },
  { label: "Orbit control API", sub: "HTTP · localhost", accent: false },
  { label: "Built-in TCP proxy", sub: "owns the host port", accent: true },
  { label: "Docker Engine", sub: "start · stop · inspect", accent: false },
  { label: "Service containers", sub: "your app · replaceable", accent: false },
];

const BOX_H = 50;
const GAP = 22;
const TOP = 12;
const step = BOX_H + GAP;
const height = TOP + layers.length * step - GAP + TOP;

export default function Architecture() {
  return (
    <section className="section" id="architecture" aria-labelledby="arch-title">
      <div className="container">
        <p className="eyebrow">Architecture</p>
        <h2 id="arch-title" className="section-title">
          One binary, one process, no moving parts to operate.
        </h2>
        <p className="section-lead">
          There is no database, no sidecar, and no external control plane. The
          binary is both the CLI you run and the proxy that holds your port —
          which means one failure domain and a cost you can measure.
        </p>

        <div className={styles.layout}>
        <div className={styles.wrap}>
          <svg
            className={styles.svg}
            viewBox={`0 0 460 ${height}`}
            role="img"
            aria-labelledby="arch-svg-title arch-svg-desc"
          >
            <title id="arch-svg-title">Orbit architecture layers</title>
            <desc id="arch-svg-desc">
              A request travels from the docker orbit command through the Docker
              CLI plugin and Orbit control API to the built-in TCP proxy, which
              drives the Docker Engine to manage your service containers.
            </desc>
            <defs>
              <marker
                id="arch-arrow"
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

            {layers.map((l, i) => {
              const y = TOP + i * step;
              return (
                <g key={l.label}>
                  {i > 0 && (
                    <line
                      x1="230"
                      y1={y - GAP}
                      x2="230"
                      y2={y - 2}
                      stroke="var(--muted)"
                      strokeWidth="1.5"
                      markerEnd="url(#arch-arrow)"
                    />
                  )}
                  <rect
                    x="70"
                    y={y}
                    width="320"
                    height={BOX_H}
                    rx="9"
                    className={l.accent ? styles.boxAccent : styles.box}
                  />
                  <text
                    x="90"
                    y={y + 22}
                    className={l.accent ? styles.labelAccent : styles.label}
                  >
                    {l.label}
                  </text>
                  <text x="90" y={y + 39} className={styles.sub}>
                    {l.sub}
                  </text>
                </g>
              );
            })}
          </svg>
        </div>

        <div className={styles.facts}>
          <h3 className={styles.factsTitle}>Runtime characteristics</h3>
          <dl className={styles.factList}>
            <div>
              <dt>Path overhead</dt>
              <dd>One in-process L4 hop between client and container.</dd>
            </div>
            <div>
              <dt>Recovery &amp; routing</dt>
              <dd>
                Microsecond-scale — recovery plan ~6.7&nbsp;µs, authority
                switch ~5.1&nbsp;µs per op.
              </dd>
            </div>
            <div>
              <dt>Only millisecond cost</dt>
              <dd>
                The deliberate <code>fsync</code> on a durable state write
                (~1.28&nbsp;ms) — correct for a state store.
              </dd>
            </div>
            <div>
              <dt>Startup</dt>
              <dd>
                Bind + control API + recovery pass in low tens of
                milliseconds; <code>status</code> sub-100&nbsp;ms.
              </dd>
            </div>
            <div>
              <dt>Failure domain</dt>
              <dd>One process. No database, no consensus, no cloud calls.</dd>
            </div>
          </dl>
          <a className={styles.factsLink} href={site.links.reliabilityReport}>
            Performance baselines
            <ExternalIcon width={14} height={14} />
          </a>
        </div>
        </div>
      </div>
    </section>
  );
}
