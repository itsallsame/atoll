import fs from 'node:fs';
import path from 'node:path';
import {fileURLToPath} from 'node:url';

const root=path.resolve(path.dirname(fileURLToPath(import.meta.url)),'..');
const read=value=>fs.readFileSync(path.join(root,value),'utf8');
const compose=read('deploy/registry/docker-compose.yml'),runtime=read('deploy/registry/registry.env.example'),nginx=read('deploy/registry/nginx-location.conf.example');
const migrations=fs.readdirSync(path.join(root,'db/migrations')).filter(name=>/^\d+.*\.sql$/u.test(name)).sort();
const checks=[
  [compose.includes('127.0.0.1:${CVMAX_REGISTRY_PORT_HOST:-8791}:8791'),'registry_port_must_be_loopback_only'],
  [compose.includes('read_only: true')&&compose.includes('cap_drop:'),'registry_container_must_be_restricted'],
  [runtime.includes('CVMAX_REGISTRY_AUTH_MODE=installation')&&!runtime.includes('CVMAX_REGISTRY_WRITE_TOKEN=')&&runtime.includes('CVMAX_REGISTRY_ADMIN_TOKEN=')&&runtime.includes('CVMAX_REGISTRY_TRUST_TLS_PROXY=true'),'production_auth_or_tls_proxy_missing'],
  [nginx.includes('location /cvmax-registry/')&&nginx.includes('proxy_pass http://127.0.0.1:8791/'),'https_gateway_route_missing'],
  [migrations.length===1&&migrations[0]==='001_recipe_registry.sql','recipe_registry_migration_missing'],
];
for(const [passed,error] of checks)if(!passed)throw new Error(error);
console.log(`[cvmax-registry] deploy contract valid; migrations=${migrations.length}`);
