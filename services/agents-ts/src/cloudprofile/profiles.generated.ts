// Generated from deploy/cloud-profiles/profiles.json by deploy/cloud-profiles/cloudprofile.py; do not edit.
export const CLOUD_PROFILE_DATA = {
  "schemaVersion": 1,
  "comment": "Single source of truth for the Passage cloud profile (#599). Go (platform/go/cloudprofile), TypeScript (services/agents-ts/src/cloudprofile) and Helm global values (deploy/cloud-profiles/values) are generated from this file by `python3 deploy/cloud-profiles/cloudprofile.py sync`; tests fail when they drift. Every endpoint carries the Microsoft Learn page it comes from. status 'unverified' means the page does not list the value and it must be confirmed before that cloud ships.",
  "endpointKeys": [
    "entraAuthority",
    "appRegistrationPortal",
    "graphRoot",
    "armEndpoint",
    "keyVaultSuffix",
    "keyVaultPrivateLinkZone",
    "blobSuffix",
    "containerRegistrySuffix",
    "postgresSuffix",
    "azureOpenAISuffix"
  ],
  "profiles": {
    "commercial": {
      "description": "Azure commercial (global) cloud, customer sign-in through Entra ID on login.microsoftonline.com. Right for SOC 2, ISO 27001 and non-regulated enterprise; not for ITAR or CUI (#600).",
      "cloud": "AzurePublic",
      "fips": false,
      "allowedRegions": [],
      "endpoints": {
        "entraAuthority": {
          "value": "https://login.microsoftonline.com",
          "source": "https://learn.microsoft.com/en-us/entra/identity-platform/authentication-national-cloud"
        },
        "appRegistrationPortal": {
          "value": "portal.azure.com",
          "source": "https://learn.microsoft.com/en-us/graph/deployments"
        },
        "graphRoot": {
          "value": "https://graph.microsoft.com",
          "source": "https://learn.microsoft.com/en-us/graph/deployments"
        },
        "armEndpoint": {
          "value": "https://management.azure.com",
          "source": "https://learn.microsoft.com/en-us/azure/azure-government/compare-azure-government-global-azure"
        },
        "keyVaultSuffix": {
          "value": "vault.azure.net",
          "source": "https://learn.microsoft.com/en-us/azure/key-vault/general/access-behind-firewall"
        },
        "blobSuffix": {
          "value": "blob.core.windows.net",
          "source": "https://learn.microsoft.com/en-us/azure/azure-government/compare-azure-government-global-azure"
        },
        "containerRegistrySuffix": {
          "value": "azurecr.io",
          "source": "https://learn.microsoft.com/en-us/azure/azure-government/compare-azure-government-global-azure"
        },
        "postgresSuffix": {
          "value": "postgres.database.azure.com",
          "source": "https://learn.microsoft.com/en-us/azure/azure-government/compare-azure-government-global-azure"
        },
        "azureOpenAISuffix": {
          "value": "openai.azure.com",
          "source": "https://learn.microsoft.com/en-us/azure/azure-government/compare-azure-government-global-azure"
        },
        "keyVaultPrivateLinkZone": {
          "value": "privatelink.vaultcore.azure.net",
          "source": "https://learn.microsoft.com/en-us/azure/private-link/private-endpoint-dns"
        }
      },
      "registry": {
        "default": "ghcr.io/benjaminsrussell",
        "requireMirror": false
      },
      "egress": {
        "llmProviders": [
          "anthropic",
          "bedrock",
          "openai",
          "azure-openai"
        ],
        "enforceProviders": false,
        "pagerDuty": true,
        "extraAllow": [
          "docker.io"
        ]
      },
      "keycloak": {
        "passwordHash": "pbkdf2-sha512",
        "passwordMinLength": 14,
        "eventRetentionDays": 90
      }
    },
    "gcc": {
      "description": "Microsoft 365 GCC. GCC is a Microsoft 365 environment on the worldwide Entra ID, so the authority and Graph are the commercial ones; Passage adds FIPS builds and US-only regions (#601). Do not switch to .us endpoints for GCC.",
      "cloud": "AzurePublic",
      "fips": true,
      "allowedRegions": [
        "eastus",
        "eastus2",
        "centralus",
        "northcentralus",
        "southcentralus",
        "westcentralus",
        "westus",
        "westus2",
        "westus3"
      ],
      "endpoints": {
        "entraAuthority": {
          "value": "https://login.microsoftonline.com",
          "source": "https://learn.microsoft.com/en-us/entra/identity-platform/authentication-national-cloud"
        },
        "appRegistrationPortal": {
          "value": "portal.azure.com",
          "source": "https://learn.microsoft.com/en-us/graph/deployments"
        },
        "graphRoot": {
          "value": "https://graph.microsoft.com",
          "source": "https://learn.microsoft.com/en-us/graph/deployments"
        },
        "armEndpoint": {
          "value": "https://management.azure.com",
          "source": "https://learn.microsoft.com/en-us/azure/azure-government/compare-azure-government-global-azure"
        },
        "keyVaultSuffix": {
          "value": "vault.azure.net",
          "source": "https://learn.microsoft.com/en-us/azure/key-vault/general/access-behind-firewall"
        },
        "blobSuffix": {
          "value": "blob.core.windows.net",
          "source": "https://learn.microsoft.com/en-us/azure/azure-government/compare-azure-government-global-azure"
        },
        "containerRegistrySuffix": {
          "value": "azurecr.io",
          "source": "https://learn.microsoft.com/en-us/azure/azure-government/compare-azure-government-global-azure"
        },
        "postgresSuffix": {
          "value": "postgres.database.azure.com",
          "source": "https://learn.microsoft.com/en-us/azure/azure-government/compare-azure-government-global-azure"
        },
        "azureOpenAISuffix": {
          "value": "openai.azure.com",
          "source": "https://learn.microsoft.com/en-us/azure/azure-government/compare-azure-government-global-azure"
        },
        "keyVaultPrivateLinkZone": {
          "value": "privatelink.vaultcore.azure.net",
          "source": "https://learn.microsoft.com/en-us/azure/private-link/private-endpoint-dns"
        }
      },
      "registry": {
        "default": "ghcr.io/benjaminsrussell",
        "requireMirror": true
      },
      "egress": {
        "llmProviders": [
          "azure-openai"
        ],
        "enforceProviders": true,
        "pagerDuty": false,
        "extraAllow": []
      },
      "keycloak": {
        "passwordHash": "pbkdf2-sha512",
        "passwordMinLength": 14,
        "eventRetentionDays": 365
      }
    },
    "gcc-high": {
      "description": "Microsoft 365 GCC High on Azure Government. Entra authority login.microsoftonline.us, Graph graph.microsoft.us. Region, registry and egress detail is filled in by the GCC High issue (#602).",
      "cloud": "AzureUSGovernment",
      "fips": true,
      "allowedRegions": [
        "usgovvirginia",
        "usgovarizona",
        "usgovtexas"
      ],
      "endpoints": {
        "entraAuthority": {
          "value": "https://login.microsoftonline.us",
          "source": "https://learn.microsoft.com/en-us/entra/identity-platform/authentication-national-cloud"
        },
        "appRegistrationPortal": {
          "value": "portal.azure.us",
          "source": "https://learn.microsoft.com/en-us/graph/deployments"
        },
        "graphRoot": {
          "value": "https://graph.microsoft.us",
          "source": "https://learn.microsoft.com/en-us/graph/deployments"
        },
        "armEndpoint": {
          "value": "https://management.usgovcloudapi.net",
          "source": "https://learn.microsoft.com/en-us/azure/azure-government/compare-azure-government-global-azure"
        },
        "keyVaultSuffix": {
          "value": "vault.usgovcloudapi.net",
          "source": "https://learn.microsoft.com/en-us/azure/key-vault/general/access-behind-firewall"
        },
        "blobSuffix": {
          "value": "blob.core.usgovcloudapi.net",
          "source": "https://learn.microsoft.com/en-us/azure/azure-government/compare-azure-government-global-azure"
        },
        "containerRegistrySuffix": {
          "value": "azurecr.us",
          "source": "https://learn.microsoft.com/en-us/azure/azure-government/compare-azure-government-global-azure"
        },
        "postgresSuffix": {
          "value": "postgres.database.usgovcloudapi.net",
          "source": "https://learn.microsoft.com/en-us/azure/azure-government/compare-azure-government-global-azure"
        },
        "azureOpenAISuffix": {
          "value": "openai.azure.us",
          "source": "https://learn.microsoft.com/en-us/azure/azure-government/compare-azure-government-global-azure"
        },
        "keyVaultPrivateLinkZone": {
          "value": "privatelink.vaultcore.usgovcloudapi.net",
          "source": "https://learn.microsoft.com/en-us/azure/private-link/private-endpoint-dns"
        }
      },
      "registry": {
        "default": "ghcr.io/benjaminsrussell",
        "requireMirror": true
      },
      "egress": {
        "llmProviders": [
          "azure-openai"
        ],
        "enforceProviders": true,
        "pagerDuty": false,
        "extraAllow": []
      },
      "keycloak": {
        "passwordHash": "pbkdf2-sha512",
        "passwordMinLength": 14,
        "eventRetentionDays": 365
      }
    },
    "dod": {
      "description": "Microsoft 365 DoD on Azure Government (IL5). Same Entra authority as GCC High; Graph is dod-graph.microsoft.us. Region and egress detail is filled in by the DoD issue (#603).",
      "cloud": "AzureUSGovernment",
      "fips": true,
      "allowedRegions": [
        "usdodeast",
        "usdodcentral",
        "usgovvirginia",
        "usgovarizona",
        "usgovtexas"
      ],
      "endpoints": {
        "entraAuthority": {
          "value": "https://login.microsoftonline.us",
          "source": "https://learn.microsoft.com/en-us/entra/identity-platform/authentication-national-cloud"
        },
        "appRegistrationPortal": {
          "value": "portal.azure.us",
          "source": "https://learn.microsoft.com/en-us/graph/deployments"
        },
        "graphRoot": {
          "value": "https://dod-graph.microsoft.us",
          "source": "https://learn.microsoft.com/en-us/graph/deployments"
        },
        "armEndpoint": {
          "value": "https://management.usgovcloudapi.net",
          "source": "https://learn.microsoft.com/en-us/azure/azure-government/compare-azure-government-global-azure"
        },
        "keyVaultSuffix": {
          "value": "vault.usgovcloudapi.net",
          "source": "https://learn.microsoft.com/en-us/azure/key-vault/general/access-behind-firewall"
        },
        "blobSuffix": {
          "value": "blob.core.usgovcloudapi.net",
          "source": "https://learn.microsoft.com/en-us/azure/azure-government/compare-azure-government-global-azure"
        },
        "containerRegistrySuffix": {
          "value": "azurecr.us",
          "source": "https://learn.microsoft.com/en-us/azure/azure-government/compare-azure-government-global-azure"
        },
        "postgresSuffix": {
          "value": "postgres.database.usgovcloudapi.net",
          "source": "https://learn.microsoft.com/en-us/azure/azure-government/compare-azure-government-global-azure"
        },
        "azureOpenAISuffix": {
          "value": "openai.azure.us",
          "source": "https://learn.microsoft.com/en-us/azure/azure-government/compare-azure-government-global-azure"
        },
        "keyVaultPrivateLinkZone": {
          "value": "privatelink.vaultcore.usgovcloudapi.net",
          "source": "https://learn.microsoft.com/en-us/azure/private-link/private-endpoint-dns"
        }
      },
      "registry": {
        "default": "ghcr.io/benjaminsrussell",
        "requireMirror": true
      },
      "egress": {
        "llmProviders": [
          "azure-openai"
        ],
        "enforceProviders": true,
        "pagerDuty": false,
        "extraAllow": []
      },
      "keycloak": {
        "passwordHash": "pbkdf2-sha512",
        "passwordMinLength": 14,
        "eventRetentionDays": 365
      }
    },
    "china": {
      "description": "Azure operated by 21Vianet. Separate Entra authority and Graph root; tokens and app registrations are not shared with any other cloud. Detail is filled in by the China issue (#605).",
      "cloud": "AzureChinaCloud",
      "fips": false,
      "allowedRegions": [
        "chinanorth",
        "chinaeast",
        "chinanorth2",
        "chinaeast2",
        "chinanorth3",
        "chinaeast3"
      ],
      "endpoints": {
        "entraAuthority": {
          "value": "https://login.partner.microsoftonline.cn",
          "source": "https://learn.microsoft.com/en-us/entra/identity-platform/authentication-national-cloud",
          "aliases": [
            "https://login.chinacloudapi.cn"
          ]
        },
        "appRegistrationPortal": {
          "value": "portal.azure.cn",
          "source": "https://learn.microsoft.com/en-us/graph/deployments"
        },
        "graphRoot": {
          "value": "https://microsoftgraph.chinacloudapi.cn",
          "source": "https://learn.microsoft.com/en-us/graph/deployments"
        },
        "armEndpoint": {
          "value": "https://management.chinacloudapi.cn",
          "source": "https://learn.microsoft.com/en-us/azure/china/resources-developer-guide"
        },
        "keyVaultSuffix": {
          "value": "vault.azure.cn",
          "source": "https://learn.microsoft.com/en-us/azure/key-vault/general/access-behind-firewall"
        },
        "blobSuffix": {
          "value": "blob.core.chinacloudapi.cn",
          "source": "https://learn.microsoft.com/en-us/azure/private-link/private-endpoint-dns"
        },
        "containerRegistrySuffix": {
          "value": "azurecr.cn",
          "source": "https://learn.microsoft.com/en-us/azure/china/resources-developer-guide",
          "status": "unverified"
        },
        "postgresSuffix": {
          "value": "postgres.database.chinacloudapi.cn",
          "source": "https://learn.microsoft.com/en-us/azure/private-link/private-endpoint-dns"
        },
        "azureOpenAISuffix": {
          "value": "openai.azure.cn",
          "source": "https://learn.microsoft.com/en-us/azure/china/resources-developer-guide",
          "status": "unverified"
        },
        "keyVaultPrivateLinkZone": {
          "value": "privatelink.vaultcore.azure.cn",
          "source": "https://learn.microsoft.com/en-us/azure/private-link/private-endpoint-dns"
        }
      },
      "registry": {
        "default": "ghcr.io/benjaminsrussell",
        "requireMirror": true
      },
      "egress": {
        "llmProviders": [
          "azure-openai"
        ],
        "enforceProviders": true,
        "pagerDuty": false,
        "extraAllow": []
      },
      "keycloak": {
        "passwordHash": "pbkdf2-sha512",
        "passwordMinLength": 14,
        "eventRetentionDays": 90
      }
    }
  },
  "overlays": {
    "eu-data-boundary": {
      "description": "EU Data Boundary: data, backups, logs and LLM calls stay in EU or EFTA regions. Cannot be combined with a profile whose regions are all outside the EU (gcc, gcc-high, dod, china).",
      "allowedRegions": [
        "northeurope",
        "westeurope",
        "francecentral",
        "francesouth",
        "germanywestcentral",
        "germanynorth",
        "swedencentral",
        "swedensouth",
        "italynorth",
        "polandcentral",
        "spaincentral",
        "norwayeast",
        "norwaywest",
        "switzerlandnorth",
        "switzerlandwest"
      ],
      "source": "https://learn.microsoft.com/en-us/privacy/eudb/eu-data-boundary-learn",
      "egressRemove": [],
      "requireFips": false
    },
    "hipaa": {
      "description": "HIPAA: egress to SaaS without a recorded business associate agreement is removed (paging, hosted LLMs other than Azure OpenAI), and audit records are kept for 6 years. Detail is filled in by the HIPAA issue.",
      "allowedRegions": [],
      "source": "https://learn.microsoft.com/en-us/azure/compliance/offerings/offering-hipaa-us",
      "egressRemove": [
        "pagerDuty",
        "anthropic",
        "bedrock",
        "openai"
      ],
      "requireFips": false,
      "eventRetentionDays": 2190
    }
  }
} as const;
