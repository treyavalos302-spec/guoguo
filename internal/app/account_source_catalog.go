package app

var accountSourceChoices = []accountSourceChoice{{ID: "hongguo", Name: "红果"}, {ID: "huangguo", Name: "黄果"}, {ID: "huangdou", Name: "黄豆"}}
var accountSourceAliases = map[string]string{"hongguo": "hongguo", "huangguo": "huangguo", "cloudfront": "huangguo", "huangguoai": "huangguo", "huangguo-video": "huangguo", "huangdou": "huangdou"}

const accountLegacySource = "huangguo"
