import styles from "./Comparison.module.css";

const cols = ["Compose recreate", "Traefik + Compose", "Kubernetes", "Orbit"];

type Cell = { v: "yes" | "no" | string };

const rows: { label: string; cells: Cell[] }[] = [
  {
    label: "Zero-downtime rollout",
    cells: [{ v: "no" }, { v: "yes" }, { v: "yes" }, { v: "yes" }],
  },
  {
    label: "No separate proxy to run",
    cells: [{ v: "yes" }, { v: "no" }, { v: "no" }, { v: "yes" }],
  },
  {
    label: "Uses your compose file as-is",
    cells: [
      { v: "yes" },
      { v: "with labels" },
      { v: "no" },
      { v: "yes" },
    ],
  },
  {
    label: "New config to learn",
    cells: [
      { v: "none" },
      { v: "proxy labels" },
      { v: "manifests" },
      { v: "one command" },
    ],
  },
  {
    label: "Extra moving parts",
    cells: [
      { v: "none" },
      { v: "a proxy" },
      { v: "a cluster" },
      { v: "one process" },
    ],
  },
  {
    label: "Fits when",
    cells: [
      { v: "downtime is fine" },
      { v: "you already run one" },
      { v: "you need a cluster" },
      { v: "Compose in prod" },
    ],
  },
];

function Mark({ v }: Cell) {
  if (v === "yes")
    return (
      <span className={`${styles.mark} ${styles.yes}`} aria-label="Yes">
        <svg viewBox="0 0 24 24" width="16" height="16" aria-hidden>
          <path
            d="M4 12.5 9 17.5 20 6.5"
            fill="none"
            stroke="currentColor"
            strokeWidth="2.2"
            strokeLinecap="round"
            strokeLinejoin="round"
          />
        </svg>
      </span>
    );
  if (v === "no")
    return (
      <span className={`${styles.mark} ${styles.no}`} aria-label="No">
        <svg viewBox="0 0 24 24" width="14" height="14" aria-hidden>
          <path
            d="M6 6l12 12M18 6L6 18"
            fill="none"
            stroke="currentColor"
            strokeWidth="2.2"
            strokeLinecap="round"
          />
        </svg>
      </span>
    );
  return <span className={styles.text}>{v}</span>;
}

export default function Comparison() {
  return (
    <section className="section" id="compare" aria-labelledby="compare-title">
      <div className="container">
        <p className="eyebrow">Where Orbit fits</p>
        <h2 id="compare-title" className="section-title">
          Four honest ways to deploy Compose.
        </h2>
        <p className="section-lead">
          Each of these is the right answer for someone. Orbit is for teams
          running Compose in production who want safe deploys without adopting a
          cluster or standing up their own reverse proxy.
        </p>

        <div className={styles.scroll} role="region" aria-label="Comparison table" tabIndex={0}>
          <table className={styles.table}>
            <caption className="sr-only">
              How Compose recreate, Traefik, Kubernetes and Orbit compare across
              deployment concerns
            </caption>
            <thead>
              <tr>
                <th scope="col" className={styles.rowHead}>
                  <span className="sr-only">Concern</span>
                </th>
                {cols.map((c) => (
                  <th
                    key={c}
                    scope="col"
                    className={c === "Orbit" ? styles.orbitCol : ""}
                  >
                    {c}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {rows.map((r) => (
                <tr key={r.label}>
                  <th scope="row" className={styles.rowHead}>
                    {r.label}
                  </th>
                  {r.cells.map((cell, i) => (
                    <td
                      key={i}
                      className={cols[i] === "Orbit" ? styles.orbitCol : ""}
                    >
                      <Mark {...cell} />
                    </td>
                  ))}
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </div>
    </section>
  );
}
