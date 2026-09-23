# CvMax Public Recipe Registry

This deploys only the CvMax public Recipe Registry. It does not add routes, state, or scheduling to Atoll or Light Cone.

## 1. Migrate

Copy `migrate.env.example` to a private `migrate.env`, use a schema-owner account, then run:

```bash
npm run registry:migrate -- deploy/registry/migrate.env
```

The runner records each migration and its checksum in `cvmax_schema_migrations`. The runtime `cvmax_app` account should retain only `SELECT`, `INSERT`, `UPDATE`, and `DELETE` on the `cvmax` database.

## 2. Start the registry

Copy `registry.env.example` to `registry.env`, insert production secrets, and run:

```bash
docker compose -f deploy/registry/docker-compose.yml up -d --build
curl -fsS http://127.0.0.1:8791/livez
curl -fsS http://127.0.0.1:8791/readyz
```

The container binds only to host loopback. Put the supplied nginx location inside the existing HTTPS server and set clients to `https://justai.cool/cvmax-registry`. Production startup rejects plaintext exposure unless `CVMAX_REGISTRY_TRUST_TLS_PROXY=true` is explicit or a certificate and key are configured directly. Production also requires installation authentication and a separate admin token.

## 3. Provision an installation

The Registry never distributes one shared write token. Generate an independent anonymous installation ID and credential with:

```bash
npm run registry:provision -- /absolute/private/registry.env
```

Store the returned values only in that installation's private Service config:

```json
{
  "recipeRegistry": {
    "baseUrl": "https://justai.cool/cvmax-registry",
    "installationId": "returned-installation-id",
    "credential": "returned-installation-credential"
  }
}
```

The server stores keyed digests rather than raw credentials. Revoking one installation does not rotate credentials for every other installation.

Production provisioning, revocation, release rollback and signing-key rotation use the authenticated operator command:

```bash
npm run registry:admin -- /absolute/private/registry.env provision
npm run registry:admin -- /absolute/private/registry.env revoke <installation-id>
npm run registry:admin -- /absolute/private/registry.env rollback <release-id> <reason>
npm run registry:admin -- /absolute/private/registry.env rotate-key <new-key-id> <private-key-file>
```

Key rotation publishes a transition signed by the previously trusted key. Clients fetch and verify that transition, so a public Recipe signed by the new key does not require a plugin or Service upgrade. Update the server's private environment to the new active key before its next restart. Every operator mutation writes a hashed-actor audit event; raw installation IDs, credentials and signing private keys are excluded.

## 4. Metrics, release and rollback checks

Completed applications submit an anonymous bounded metric containing coarse fact-count buckets, Recipe/adaptive path, outcome, Agent takeover and wall-clock durations. Metrics exclude task IDs, resume IDs, field values, attachment data and full URLs. The same per-installation credential and rate limit protect candidate, feedback and metric writes.

Before switching nginx traffic, require `/readyz`, the complete test suite, and a clean-instance signed-release fetch. Keep the previous image tag. Rollback changes only the Registry container and nginx route; public releases already marked `released` remain immutable, and a bad release is changed to `suspended` or `rolled_back` rather than overwritten.
