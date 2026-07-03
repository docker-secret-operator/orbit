import { site } from "@/lib/site";
import { ExternalIcon } from "./icons";
import styles from "./ProductionConsiderations.module.css";

type QA = {
  q: string;
  a: React.ReactNode;
  href?: string;
  hrefLabel?: string;
  tone?: "default" | "caution";
};

const items: QA[] = [
  {
    q: "What happens if the proxy or host crashes?",
    a: (
      <>
        On restart, Orbit re-runs recovery from its persisted on-disk authority
        and reconciles the live backends — it never guesses. Atomic writes
        survive a crash mid-write, and recovery decisions are deterministic
        (byte-identical across 50 repeats per scenario in tests). Run the proxy
        under Docker&apos;s <code>restart: always</code> policy so it returns
        automatically.
      </>
    ),
    href: site.links.reliabilityReport,
    hrefLabel: "Crash-recovery report",
  },
  {
    q: "What if a new build fails its health check?",
    a: (
      <>
        The new backend never registers with the proxy; it is scaled back down
        and the old backend is left untouched. No partial state, no dropped
        traffic — the deploy exits non-zero and nothing needs rolling back.
      </>
    ),
    href: site.links.reliabilityReport,
    hrefLabel: "Failure matrix",
  },
  {
    q: "Is there a rollback?",
    a: (
      <>
        <code>docker orbit rollback</code> promotes the one persisted prior
        generation back behind the proxy. A history event is recorded on every
        outcome — success or failure — so even a crash mid-rollback leaves a
        record. One generation is retained, not an unbounded history.
      </>
    ),
    href: site.links.cliReference,
    hrefLabel: "CLI reference",
  },
  {
    q: "Can it corrupt deployment state?",
    a: (
      <>
        State, history, and lock files are written <code>0600</code> with a
        unique temp file, <code>fsync</code>, and atomic rename. The state
        package and the full 25-scenario chaos suite pass under the Go race
        detector; state-corruption scenarios fail safe rather than tear.
      </>
    ),
    href: site.links.reliabilityReport,
    hrefLabel: "Chaos & concurrency report",
  },
  {
    q: "Is the proxy a single point of failure?",
    a: (
      <>
        Honestly: it sits in the traffic path and owns the host port — that is
        the design. It is a single in-process L4 proxy with no external
        dependencies (no database, no consensus). If it dies, recovery
        reconciles state on restart; there is no multi-node clustering. Keep it
        under a restart policy and treat it as you would any edge proxy.
      </>
    ),
    href: site.links.constitution,
    hrefLabel: "Architecture boundaries",
  },
  {
    q: "What is the security posture — really?",
    tone: "caution",
    a: (
      <>
        Files are <code>0600</code>, the API token is never logged, and token
        auth is enforced when set. But by default the control API binds all
        interfaces and authentication is opt-in. Set <code>ORBIT_API_TOKEN</code>{" "}
        and/or bind it to localhost before exposing a host. This is the single
        item keeping Orbit at <strong>release candidate</strong> rather than
        production-ready — and it is documented, not hidden.
      </>
    ),
    href: site.links.reliabilityReport,
    hrefLabel: "Security review",
  },
];

export default function ProductionConsiderations() {
  return (
    <section
      className="section"
      id="production"
      aria-labelledby="production-title"
    >
      <div className="container">
        <p className="eyebrow">Built for production</p>
        <h2 id="production-title" className="section-title">
          The questions you&apos;d ask before trusting it with your port.
        </h2>
        <p className="section-lead">
          Every answer below is behavior verified by tests in the repository,
          not a marketing claim. Follow the links to the source.
        </p>

        <dl className={styles.list}>
          {items.map((it) => (
            <div
              key={it.q}
              className={`${styles.item} ${
                it.tone === "caution" ? styles.caution : ""
              }`}
            >
              <dt className={styles.q}>{it.q}</dt>
              <dd className={styles.a}>
                <p>{it.a}</p>
                {it.href && (
                  <a className={styles.link} href={it.href}>
                    {it.hrefLabel}
                    <ExternalIcon width={14} height={14} />
                  </a>
                )}
              </dd>
            </div>
          ))}
        </dl>
      </div>
    </section>
  );
}
