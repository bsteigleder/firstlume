import { logos } from "./serviceLogos";

// What a resource is, from its Terraform type: a known service shows its logo
// in white on the brand color; a generic kind (VM, disk, network…) shows a
// line glyph tinted by its provider. Data sources get a dashed, empty tile.

type Glyph = keyof typeof glyphs;
type Tint = "lxd" | "aws" | "plain";

const glyphs = {
  network: "M4 4h16v6H4zM12 10v4M5 14h14M5 14v3M12 14v3M19 14v3M3 17h4v3H3zM10 17h4v3h-4zM17 17h4v3h-4z",
  vm: "M4 4h16v7H4zM4 13h16v7H4zM8 7.5h.01M8 16.5h.01M12 7.5h4M12 16.5h4",
  disk: "M4 6c0-1.7 3.6-3 8-3s8 1.3 8 3-3.6 3-8 3-8-1.3-8-3zM4 6v12c0 1.7 3.6 3 8 3s8-1.3 8-3V6M4 12c0 1.7 3.6 3 8 3s8-1.3 8-3",
  profile: "M4 7h9M17 7h3M4 17h3M11 17h9M15 5v4M9 15v4",
  command: "M3 5h18v14H3zM7 10l3 2-3 2M12 15h4",
  file: "M14 3H6v18h12V7zM14 3v4h4",
  secretFile: "M14 3H6v18h12V7zM14 3v4h4M9.5 14h5v4h-5zM10.5 14v-1.5a1.5 1.5 0 0 1 3 0V14",
  firewall: "M3 5h18v14H3zM3 10h18M3 15h18M9 5v5M15 10v5M9 15v4",
  route: "M5 19h.01M19 5h.01M5 17V10a5 5 0 0 1 5-5h7M19 7v7a5 5 0 0 1-5 5H7",
  dns: "M3 12a9 9 0 1 0 18 0a9 9 0 1 0-18 0M3 12h18M12 3c2.5 2.5 3.8 5.5 3.8 9s-1.3 6.5-3.8 9c-2.5-2.5-3.8-5.5-3.8-9S9.5 5.5 12 3z",
  key: "M3 12a4 4 0 1 0 8 0a4 4 0 1 0-8 0M11 12h10M17 12v3M20 12v2",
  random: "M4 4h16v16H4zM8.5 8.5h.01M15.5 8.5h.01M12 12h.01M8.5 15.5h.01M15.5 15.5h.01",
  box: "M12 3l8 4.5v9L12 21l-8-4.5v-9zM12 12l8-4.5M12 12v9M12 12L4 7.5",
} as const;

type Look = { kind: string; logo?: keyof typeof logos; glyph?: Glyph };

// Exact types first, then the longest matching prefix.
const exact: Record<string, Look> = {
  aws_instance: { kind: "EC2 instance", logo: "amazonec2" },
  aws_eks_cluster: { kind: "EKS cluster", logo: "amazoneks" },
  aws_eks_node_group: { kind: "EKS node group", logo: "amazoneks" },
  aws_eks_addon: { kind: "EKS add-on", logo: "amazoneks" },
  aws_db_instance: { kind: "RDS database", logo: "amazonrds" },
  aws_rds_cluster: { kind: "RDS cluster", logo: "amazonrds" },
  aws_s3_bucket: { kind: "S3 bucket", logo: "amazons3" },
  aws_iam_role: { kind: "IAM role", logo: "amazoniam" },
  aws_iam_policy: { kind: "IAM policy", logo: "amazoniam" },
  aws_iam_user: { kind: "IAM user", logo: "amazoniam" },
  aws_route53_zone: { kind: "Route 53 zone", logo: "amazonroute53" },
  aws_route53_record: { kind: "Route 53 record", logo: "amazonroute53" },
  aws_vpc: { kind: "VPC", glyph: "network" },
  aws_subnet: { kind: "Subnet", glyph: "network" },
  aws_security_group: { kind: "Security group", glyph: "firewall" },
  aws_ebs_volume: { kind: "EBS volume", glyph: "disk" },
  cloudflare_zone: { kind: "Cloudflare zone", logo: "cloudflare" },
  cloudflare_record: { kind: "Cloudflare DNS record", logo: "cloudflare" },
  cloudflare_dns_record: { kind: "Cloudflare DNS record", logo: "cloudflare" },
  postgresql_database: { kind: "PostgreSQL database", logo: "postgresql" },
  postgresql_role: { kind: "PostgreSQL role", logo: "postgresql" },
  postgresql_grant: { kind: "PostgreSQL grant", logo: "postgresql" },
  mysql_database: { kind: "MySQL database", logo: "mysql" },
  mysql_user: { kind: "MySQL user", logo: "mysql" },
  helm_release: { kind: "Helm chart", logo: "helm" },
  kubernetes_namespace: { kind: "Kubernetes namespace", logo: "kubernetes" },
  kubernetes_namespace_v1: { kind: "Kubernetes namespace", logo: "kubernetes" },
  kubernetes_secret: { kind: "Kubernetes secret", logo: "kubernetes" },
  kubernetes_secret_v1: { kind: "Kubernetes secret", logo: "kubernetes" },
  kubernetes_manifest: { kind: "Kubernetes manifest", logo: "kubernetes" },
  vault_policy: { kind: "Vault policy", logo: "vault" },
  vault_kv_secret_v2: { kind: "Vault secret", logo: "vault" },
  vault_jwt_auth_backend_role: { kind: "Vault auth role", logo: "vault" },
  lxd_instance: { kind: "Virtual machine", glyph: "vm" },
  lxd_network: { kind: "Network", glyph: "network" },
  lxd_volume: { kind: "Disk", glyph: "disk" },
  lxd_profile: { kind: "Instance profile", glyph: "profile" },
  null_resource: { kind: "Command", glyph: "command" },
  terraform_data: { kind: "Command", glyph: "command" },
  terraform_remote_state: { kind: "Remote state", logo: "terraform" },
  local_file: { kind: "File", glyph: "file" },
  local_sensitive_file: { kind: "Sensitive file", glyph: "secretFile" },
  dns_a_record_set: { kind: "DNS record", glyph: "dns" },
  tls_private_key: { kind: "Private key", glyph: "key" },
};

const prefixes: [string, Omit<Look, "kind">][] = [
  ["aws_eks_", { logo: "amazoneks" }],
  ["aws_db_", { logo: "amazonrds" }],
  ["aws_rds_", { logo: "amazonrds" }],
  ["aws_s3_", { logo: "amazons3" }],
  ["aws_iam_", { logo: "amazoniam" }],
  ["aws_route53_", { logo: "amazonroute53" }],
  ["aws_security_group", { glyph: "firewall" }],
  ["aws_route", { glyph: "route" }],
  ["aws_nat_", { glyph: "route" }],
  ["aws_internet_gateway", { glyph: "route" }],
  ["aws_vpc", { glyph: "network" }],
  ["aws_subnet", { glyph: "network" }],
  ["aws_", { logo: "amazonwebservices" }],
  ["google_", { logo: "googlecloud" }],
  ["cloudflare_", { logo: "cloudflare" }],
  ["postgresql_", { logo: "postgresql" }],
  ["mysql_", { logo: "mysql" }],
  ["redis", { logo: "redis" }],
  ["helm_", { logo: "helm" }],
  ["kubernetes_", { logo: "kubernetes" }],
  ["vault_", { logo: "vault" }],
  ["lxd_storage", { glyph: "disk" }],
  ["lxd_network", { glyph: "network" }],
  ["lxd_", { glyph: "vm" }],
  ["dns_", { glyph: "dns" }],
  ["tls_", { glyph: "key" }],
  ["random_", { glyph: "random" }],
  ["local_", { glyph: "file" }],
];

/** A readable name for a type with no entry: aws_lb_listener → "Lb listener". */
function humanize(type: string): string {
  const rest = type.split("_").slice(1).join(" ") || type;
  return rest.charAt(0).toUpperCase() + rest.slice(1);
}

function lookOf(type: string): Look {
  const hit = exact[type];
  if (hit) return hit;
  const p = prefixes.find(([prefix]) => type.startsWith(prefix));
  return { kind: humanize(type), ...(p ? p[1] : { glyph: "box" }) };
}

export function resourceKind(type: string): string {
  return lookOf(type).kind;
}

function tintOf(type: string): Tint {
  if (type.startsWith("lxd_")) return "lxd";
  if (type.startsWith("aws_")) return "aws";
  return "plain";
}

/** The provider's short name for a source like registry.terraform.io/hashicorp/aws. */
export function providerName(source: string): string {
  return source.split("/").pop() || source;
}

const providerColors: Record<string, string> = {
  aws: logos.amazonec2.color,
  google: logos.googlecloud.color,
  cloudflare: logos.cloudflare.color,
  postgresql: logos.postgresql.color,
  mysql: logos.mysql.color,
  helm: logos.helm.color,
  kubernetes: logos.kubernetes.color,
  vault: "#B39700",
  lxd: "var(--accent)",
};

/** The color a provider shows in filters: its main service's brand color. */
export function providerColor(source: string): string {
  return providerColors[providerName(source)] ?? "var(--border-strong)";
}

export function ResourceIcon({ type, data = false, size = 30 }: { type: string; data?: boolean; size?: number }) {
  const look = lookOf(type);
  const logo = look.logo ? logos[look.logo] : null;
  const cls = ["res-icon", data ? "data" : logo ? "brand" : `tint-${tintOf(type)}`].join(" ");
  const style = logo && !data ? { background: logo.color } : undefined;
  const inner = Math.round(size * 0.56);
  return (
    <span className={cls} style={{ width: size, height: size, ...style }} aria-hidden>
      <svg width={inner} height={inner} viewBox="0 0 24 24" strokeWidth={1.8} strokeLinecap="round" strokeLinejoin="round">
        {logo ? (
          <path d={logo.d} fill={data ? (look.logo === "vault" ? "currentColor" : logo.color) : logo.ink} />
        ) : (
          <path d={glyphs[look.glyph ?? "box"]} fill="none" stroke="currentColor" />
        )}
      </svg>
    </span>
  );
}
