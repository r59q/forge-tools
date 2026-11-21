/** Fields sent to management for one connection attempt. */
export interface ConnectionDetails {
  forge: string;
  host: string;
  name: string;
  credential: string;
}

/** Public handoff after management accepts a forge connection. */
export interface AcceptedConnection {
  id: string;
}

type Submission = { connection: AcceptedConnection } | { error: string };

function problem(status: number): string {
  switch (status) {
    case 400:
      return "The forge rejected the token or the details are invalid. Check them and try again.";
    case 401:
    case 403:
      return "Operator authorization is required. Sign in and try again.";
    case 501:
      return "The management service has no connector configured.";
    default:
      return "Could not confirm the connection. Try again.";
  }
}

/** Submit to management and translate responses into a public ID or a fixed error. */
export async function addConnection(
  managementFetch: typeof fetch,
  details: ConnectionDetails,
): Promise<Submission> {
  try {
    const response = await managementFetch("/v1/connections", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(details),
      cache: "no-store",
      redirect: "error",
    });
    if (!response.ok) {
      return { error: problem(response.status) };
    }

    const result = await response.json();
    if (typeof result?.id === "string" && result.id.trim()) {
      return { connection: { id: result.id } };
    }
  } catch {
    // Transport and decoding errors can contain request or response data.
  }

  return { error: problem(0) };
}
