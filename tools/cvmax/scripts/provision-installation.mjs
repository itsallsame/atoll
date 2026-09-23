import {randomBytes, randomUUID} from 'node:crypto';
import {MysqlRecipeRegistry} from '../src/mysql-recipe-registry.mjs';
import {RecipeRegistryApp} from '../src/recipe-registry-app.mjs';
import {loadEnv} from '../src/registry-server.mjs';

const env=loadEnv(process.argv[2]),installationId=process.argv[3]??randomUUID(),credential=randomBytes(32).toString('base64url');
const repository=MysqlRecipeRegistry.fromEnv(env);
try {
  const app=new RecipeRegistryApp({repository,privateKey:'provisioning-only',signingKeyId:'provisioning-only',installationPepper:env.CVMAX_REGISTRY_INSTALLATION_PEPPER});
  await repository.provisionInstallation({installationHash:app.installationHash(installationId),credential,pepper:env.CVMAX_REGISTRY_INSTALLATION_PEPPER});
  process.stdout.write(`${JSON.stringify({installationId,credential})}\n`);
} finally { await repository.close(); }
