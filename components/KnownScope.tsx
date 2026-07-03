import { site } from "@/lib/site";
import styles from "./KnownScope.module.css";

const notList = [
  {
    t: "Route by HTTP path or host",
    d: "It is an L4 (TCP) proxy. It moves connections between backends; it does not read HTTP. No path- or host-based routing.",
  },
  {
    t: "Terminate TLS",
    d: "Terminate TLS in your app or an upstream edge. Orbit forwards the connection as-is.",
  },
  {
    t: "Replace Kubernetes",
    d: "No scheduling, autoscaling, bin-packing, or multi-node orchestration. One host, your Compose stack.",
  },
  {
    t: "Act as a service mesh",
    d: "No sidecars, no mTLS, no traffic-splitting policy. One proxy in front of one service.",
  },
  {
    t: "Run across a cluster",
    d: "Single-host by design — no distributed consensus and no external database to operate.",
  },
  {
    t: "Keep a deep rollback history",
    d: "Exactly one prior generation is retained for rollback, not an unbounded timeline.",
  },
];

export default function KnownScope() {
  return (
    <section className="section" id="scope" aria-labelledby="scope-title">
      <div className="container">
        <p className="eyebrow">Known scope</p>
        <h2 id="scope-title" className="section-title">
          What Orbit does not do.
        </h2>
        <p className="section-lead">
          Orbit adds one capability to Compose and refuses the rest on purpose.
          Knowing where it stops is part of trusting where it works.
        </p>

        <ul className={styles.list}>
          {notList.map((n) => (
            <li key={n.t} className={styles.item}>
              <span className={styles.mark} aria-hidden>
                —
              </span>
              <div>
                <h3 className={styles.itemTitle}>{n.t}</h3>
                <p className={styles.itemDesc}>{n.d}</p>
              </div>
            </li>
          ))}
        </ul>

        <p className={styles.note}>
          The full list of non-goals is fixed in the{" "}
          <a href={site.links.constitution}>project constitution</a>.
        </p>
      </div>
    </section>
  );
}
