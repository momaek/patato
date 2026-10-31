/// <reference types="vite/client" />
interface ImportMetaEnv {
  /** 服务名，登录前显示；登录后以服务端返回的为准。 */
  readonly VITE_SERVICE_NAME?: string
  /** 客服联系方式，显示在登录页和找回密码页底部。 */
  readonly VITE_SUPPORT_CONTACT?: string
}
