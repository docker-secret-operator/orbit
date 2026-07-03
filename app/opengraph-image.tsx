import { ImageResponse } from "next/og";

export const alt = "Orbit — zero-downtime deployments for Docker Compose";
export const size = { width: 1200, height: 630 };
export const contentType = "image/png";

export default function OgImage() {
  return new ImageResponse(
    (
      <div
        style={{
          width: "100%",
          height: "100%",
          display: "flex",
          flexDirection: "column",
          justifyContent: "space-between",
          background: "#0b0d0e",
          color: "#f1f3f4",
          padding: "72px",
          fontFamily: "sans-serif",
        }}
      >
        <div style={{ display: "flex", alignItems: "center", gap: 16 }}>
          <div
            style={{
              width: 40,
              height: 40,
              borderRadius: 10,
              background: "#5b9bff",
            }}
          />
          <div style={{ fontSize: 34, fontWeight: 700 }}>Orbit</div>
        </div>

        <div style={{ display: "flex", flexDirection: "column", gap: 24 }}>
          <div
            style={{
              fontSize: 74,
              fontWeight: 700,
              lineHeight: 1.05,
              letterSpacing: "-0.03em",
              maxWidth: 960,
            }}
          >
            Zero-downtime deploys for Docker Compose.
          </div>
          <div style={{ fontSize: 30, color: "#b9c0c6", maxWidth: 900 }}>
            A single binary. No Kubernetes, no Traefik, no compose rewrite.
          </div>
        </div>

        <div
          style={{
            fontSize: 26,
            color: "#5b9bff",
            fontFamily: "monospace",
          }}
        >
          $ docker orbit rollout web
        </div>
      </div>
    ),
    size,
  );
}
