const clean = value => String(value ?? '').replace(/\s+/g, ' ').trim();

export const resumeReplacementSignal = text => /(?:上传|导入).{0,20}简历.{0,30}(?:替换|覆盖).{0,20}(?:继续|确认)|(?:replace|overwrite).{0,30}resume.{0,30}(?:continue|confirm)/i.test(clean(text));
export const resumeUploadAcknowledgementSignal = text => /(?:简历|履历).{0,16}(?:上传|导入).{0,10}成功.{0,30}(?:核对|检查|确认)|(?:upload|import).{0,12}(?:resume|cv).{0,12}success/i.test(clean(text));
export const resumeParseConfirmationSignal = text => /(?:上传|导入).{0,20}(?:附件|简历|履历).{0,40}(?:自动|智能).{0,12}(?:填写|填充|解析)|(?:附件|简历|履历).{0,24}(?:上传|导入).{0,24}(?:解析|填写|填充)|(?:parse|autofill).{0,24}(?:uploaded|attached).{0,12}(?:resume|cv)/i.test(clean(text));

const acknowledgementLabel = text => /^(?:确定|确认|继续|(?:我)?知道了|好的|ok|continue|confirm)$/i.test(clean(text));
const resumeParseConfirmationLabel = text => /^(?:是[，,、 ]*)?(?:上传|导入)(?:并|后)?(?:自动|智能)?(?:解析|填写|填充)|^(?:upload|import)(?:\s+and|\s+then)?\s+(?:parse|autofill)$/i.test(clean(text));
const protectedLabel = text => /(?:最终)?提交(?:申请|投递)?|确认投递|支付|删除|撤回|签署|(?:我已阅读并)?同意(?:.{0,8}(?:条款|协议))?|submit|pay|delete|withdraw|sign|agree/i.test(clean(text));
const legalConsentLabel = text => /我已阅读并同意|同意.{0,12}(?:条款|协议|隐私|政策)|(?:agree|consent).{0,16}(?:terms|agreement|privacy|policy)/i.test(clean(text));
const informationalAcknowledgementSignal = text => /提示|提醒|须知|注意|上限|限制|谨慎|知道了|notice|reminder|limit|acknowledge/i.test(clean(text));

export function bootstrapInteraction(snapshot, predecessorKind = null) {
  if (!snapshot?.activeDialog || !['upload', 'native_parse', 'start', 'next'].includes(predecessorKind)) return null;
  const text = snapshot.activeDialog.text;
  const workflowAcknowledgement = ['start', 'next'].includes(predecessorKind) && informationalAcknowledgementSignal(text) && !protectedLabel(text) && !legalConsentLabel(text);
  const purpose = resumeReplacementSignal(text) ? 'resume_replacement' : resumeUploadAcknowledgementSignal(text) ? 'resume_upload_acknowledgement' : resumeParseConfirmationSignal(text) ? 'resume_parse_confirmation' : workflowAcknowledgement ? 'workflow_acknowledgement' : null;
  if (!purpose) return null;
  const source = snapshot.interactions?.length ? snapshot.interactions : snapshot.controls ?? [];
  const labelMatches = purpose === 'resume_parse_confirmation' ? resumeParseConfirmationLabel : acknowledgementLabel;
  const candidates = source.filter(control => control.safe === true && (control.surface === 'dialog' || !snapshot.interactions?.length) && labelMatches(control.label) && !protectedLabel(control.label));
  if (candidates.length !== 1) return null;
  return {kind: 'activate', controlRef: candidates[0].ref, activationMode: ['resume_replacement', 'workflow_acknowledgement'].includes(purpose) ? 'main_world_pointer' : 'isolated_pointer', expectedPostcondition: purpose === 'resume_replacement' ? 'surface_changed' : 'dialog_closed', purpose};
}

export function interactionProtected(control) {
  return !control?.safe || control.formSubmit === true || protectedLabel(control.label);
}

export function legalConsentInteraction(control) {
  return control?.safe === true && control.formSubmit !== true && legalConsentLabel(control.label);
}
