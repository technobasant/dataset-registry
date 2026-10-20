// Demo personas at Lumen & Loom. The persona decides which token the UI
// uses; it is a demo device, not authentication.
export interface Persona {
  id: string;
  subject: string;
  role: "admin" | "owner" | "consumer";
  team: string;
  label: string;
}

export const personas: Persona[] = [
  { id: "noor", subject: "noor.haddad", role: "owner", team: "analytics-engineering", label: "Noor Haddad — owner, analytics-engineering" },
  { id: "amara", subject: "amara.okafor", role: "owner", team: "checkout", label: "Amara Okafor — owner, checkout" },
  { id: "mateo", subject: "mateo.silva", role: "owner", team: "fulfilment", label: "Mateo Silva — owner, fulfilment" },
  { id: "jun", subject: "jun.park", role: "consumer", team: "finance-analytics", label: "Jun Park — consumer, finance-analytics" },
  { id: "hana", subject: "hana.sato", role: "consumer", team: "growth", label: "Hana Sato — consumer, growth" },
  { id: "ravi", subject: "ravi.menon", role: "consumer", team: "recommendations", label: "Ravi Menon — consumer, recommendations" },
  { id: "ines", subject: "ines.moreau", role: "admin", team: "data-platform", label: "Ines Moreau — admin, data-platform" },
  { id: "guest", subject: "", role: "consumer", team: "", label: "Not signed in (read only)" },
];

export const defaultPersona = "noor";

export function personaById(id: string | undefined): Persona {
  return personas.find((p) => p.id === id) ?? personas.find((p) => p.id === defaultPersona)!;
}
