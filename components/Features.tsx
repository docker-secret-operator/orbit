import styles from "./Features.module.css";

const features = [
  {
    t: "Health-gated rollout",
    d: "A new build only takes traffic after its health check passes — no dropped connections on a good deploy.",
  },
  {
    t: "Fails safe",
    d: "Any failure before the new backend registers leaves the old one serving. Availability wins over the deploy.",
  },
  {
    t: "Deterministic recovery",
    d: "Crash mid-deploy and a restart reconciles from on-disk authority — it never guesses at the safe state.",
  },
  {
    t: "Built-in L4 proxy",
    d: "One in-process TCP proxy owns the host port. Nothing external to run, secure, or keep in sync.",
  },
  {
    t: "Compose-native",
    d: "Reads your docker-compose.yml unmodified; databases pass through untouched. Turn Orbit off anytime.",
  },
  {
    t: "Single binary",
    d: "No database, no consensus, no cloud services. Installs as a docker CLI plugin and stays out of the way.",
  },
];

export default function Features() {
  return (
    <section className="section" id="features" aria-labelledby="features-title">
      <div className="container">
        <p className="eyebrow">What it does</p>
        <h2 id="features-title" className="section-title">
          One capability, carefully built.
        </h2>
        <p className="section-lead">
          Orbit adds exactly what Compose is missing for production — rolling
          deployments — and resists becoming anything else.
        </p>

        <ul className={styles.grid}>
          {features.map((f) => (
            <li key={f.t} className={styles.card}>
              <span className={styles.tick} aria-hidden />
              <h3 className={styles.cardTitle}>{f.t}</h3>
              <p className={styles.cardDesc}>{f.d}</p>
            </li>
          ))}
        </ul>
      </div>
    </section>
  );
}
