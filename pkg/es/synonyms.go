package es

// DomainSynonyms defines synonym rules for the keyword/domain search analyzer.
// Format: Solr-style comma-separated equivalence classes.
// All terms in a line are treated as interchangeable at search time.
//
// These are loaded inline into the ES index template (no file path dependency).
// After updating, a new index must be created (ILM rollover or reindex) for
// changes to take effect on existing data.
var DomainSynonyms = []string{
	// --- Venture capital / investment ---
	"vc, venture-capital, venture capital",
	"pe, private-equity, private equity",
	"lp, limited-partner, limited partner",
	"gp, general-partner, general partner",
	"deal-flow, dealflow",
	"technical-dd, technical due diligence, tech-dd",
	"early-stage, seed-stage, pre-seed",

	// --- Crypto / Web3 ---
	"crypto, web3, blockchain, cryptocurrency",
	"crypto-infra, blockchain-infra, blockchain infrastructure",
	"defi, decentralized-finance, decentralized finance",
	"nft, non-fungible-token, non fungible token",
	"tokenization, token-issuance",
	"stablecoin, stablecoins",

	// --- AI / ML ---
	"ai-agents, agent-systems, ai agents, agent systems",
	"ai-infra, ai-infrastructure, ai infrastructure",
	"llm, large-language-model, large language model",
	"rag, retrieval-augmented-generation, retrieval augmented generation",
	"mlops, ml-ops, ml operations",
	"nlp, natural-language-processing, natural language processing",
	"genai, generative-ai, generative ai",
	"computer-vision, cv, machine-vision",

	// --- Cloud / DevOps ---
	"devops, dev-ops",
	"cloud-security, cloudsec",
	"cloud-native, cloudnative",
	"k8s, kubernetes",
	"ci-cd, cicd, continuous-integration",
	"iac, infrastructure-as-code, infrastructure as code",
	"sre, site-reliability-engineering, site reliability engineering",

	// --- Data ---
	"data-engineering, data engineering, dataeng",
	"data-science, data science",
	"data-analytics, data analysis, data-analysis",
	"etl, extract-transform-load",
	"data-lakehouse, lakehouse",

	// --- E-commerce ---
	"e-commerce, ecommerce, electronic-commerce",
	"cross-border-ecommerce, cross-border e-commerce",
	"d2c, dtc, direct-to-consumer",
	"supply-chain, supplychain",

	// --- Fintech ---
	"fintech, financial-technology, financial technology",
	"open-banking, openbanking",
	"insurtech, insurance-technology",
	"regtech, regulatory-technology",
	"payments, payment-rails",

	// --- Healthcare ---
	"healthtech, health-tech, healthcare-technology",
	"biotech, biotechnology",
	"medtech, medical-technology",
	"digital-health, digital health",

	// --- SaaS / Product ---
	"saas, software-as-a-service",
	"plg, product-led-growth, product led growth",
	"b2b, business-to-business",
	"b2c, business-to-consumer",

	// --- Geopolitics / Macro ---
	"geopolitics, geo-politics, geopolitical",
	"macro, macroeconomics",
}
