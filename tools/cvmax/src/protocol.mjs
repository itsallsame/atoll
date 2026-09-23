export const CVMAX_PROTOCOL = Object.freeze({
  client: {current: 1, minimum: 1},
  service: {current: 2, minimum: 2},
  extension: {current: 6, minimum: 6},
  recipe: {current: 1, minimum: 1},
});

export function protocolAdvertisement() {
  return Object.fromEntries(Object.entries(CVMAX_PROTOCOL).map(([name,value])=>[name,{...value}]));
}

export function assertProtocolCompatible(name, peer) {
  const local=CVMAX_PROTOCOL[name];
  if(!local||!peer||!Number.isSafeInteger(peer.current)||!Number.isSafeInteger(peer.minimum))throw Error(`protocol_${name}_missing`);
  if(peer.current<local.minimum||local.current<peer.minimum)throw Error(`protocol_${name}_incompatible`);
  return true;
}
